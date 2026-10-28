//go:build darwin

package darwin

// Page-embedded web views: child WKWebViews behind the page of a window.
// Each page leaves a transparent hole where its child shows through, the
// host web view's hitTest: lets the pointer through in that hole unless
// the page says a dialog covers it, and each child is clipped to its hole.
// This is the macOS cut of the layout Electron apps use for <webview>: the
// page places the children, its own dialogs and backdrop paint over them,
// and the native side converts the page's viewport rectangles to the
// content view's coordinates, runs the children's scripts, and routes
// their messages and navigations.

import (
	"encoding/json"
	"errors"
	"log"

	"github.com/ebitengine/purego/objc"

	"github.com/egoist/mygo/internal/platform"
)

const (
	// embeddedMessageName is the name the pages post their messages to,
	// window.webkit.messageHandlers.mygoEmbedded.
	embeddedMessageName = "mygoEmbedded"
	// embeddedEventName is the DOM event PostMessage dispatches in the
	// child page, whose detail is the message.
	embeddedEventName = "mygo:message"
)

// embeddedPageScript gives the child page its side of the messaging:
// window.mygoEmbedded.postMessage sends a string to the app, and
// window.mygoEmbedded.onMessage listens to the strings the app posts.
const embeddedPageScript = `(() => {
	const handlers = window.webkit && window.webkit.messageHandlers;
	const post = handlers && handlers.` + embeddedMessageName + `;
	if (!post || window.mygoEmbedded) {
		return;
	}
	Object.defineProperty(window, 'mygoEmbedded', {
		configurable: false,
		writable: false,
		value: Object.freeze({
			postMessage(s) { post.postMessage(String(s)); },
			onMessage(fn) {
				const h = e => fn(e.detail);
				window.addEventListener('` + embeddedEventName + `', h);
				return () => window.removeEventListener('` + embeddedEventName + `', h);
			}
		})
	});
})();`

// embedded is a child web view behind the page of window host.
type embedded struct {
	host *window
	web  id
	// clip holds web, cut to the hole.
	clip id
	// ucc is the child's own content controller, which holds its script
	// and its message handler.
	ucc id
	// box is the hole in the content view's coordinates, whose origin is
	// the bottom-left corner. Its size is zero while the page shows no
	// hole.
	box NSRect
	// input tells whether the hole takes the pointer; the page clears it
	// while a dialog or backdrop covers the hole.
	input  bool
	closed bool
	// onMessage, onDidLoad and onLoadFailed are the public layer's
	// callbacks; they run on the main thread.
	onMessage    func(string)
	onDidLoad    func(string)
	onLoadFailed func(error)
}

var (
	errEmbeddedClosed      = errors.New("mygo: the window is closed")
	errEmbeddedNoPage      = errors.New("mygo: the window shows native content, not a web page")
	errEmbeddedUnsupported = errors.New("mygo: this macOS version cannot make a page transparent (_setDrawsBackground: is missing)")
)

// embedHandlerObject returns the object the children share as their
// WKScriptMessageHandler and WKNavigationDelegate, which finds the child
// in each message's web view.
func (b *Backend) embedHandlerObject() id {
	if b.embedHandler == 0 {
		b.embedHandler = alloc("MyGoEmbedHandler")
	}
	return b.embedHandler
}

// NewEmbeddedWebView creates a child web view behind w's page.
func (w *window) NewEmbeddedWebView(o platform.EmbeddedWebViewOptions) (platform.EmbeddedWebView, error) {
	if w.closed {
		return nil, errEmbeddedClosed
	}
	if w.web == 0 || w.view == 0 {
		// A window showing Content has no page to put a hole in.
		return nil, errEmbeddedNoPage
	}
	// The layout needs a transparent page: where the page paints nothing,
	// the children behind it must show. Without the private setter the
	// page would sit opaque over them.
	if !respondsTo(w.web, "_setDrawsBackground:") {
		return nil, errEmbeddedUnsupported
	}
	e := &embedded{host: w, input: true}
	withPool(func() {
		if len(w.embeds) == 0 {
			// The page becomes transparent once, for its first child.
			send(w.web, "_setDrawsBackground:", 0)
			send(w.web, "setValue:forKey:", uintptr(nsBool(false)), uintptr(nsString("drawsBackground")))
			if respondsTo(w.web, "setUnderPageBackgroundColor:") {
				send(w.web, "setUnderPageBackgroundColor:", uintptr(send(class("NSColor"), "clearColor")))
			}
		}

		cfg := alloc("WKWebViewConfiguration")
		if o.DataStore != nil {
			// The child's profile: its session's store (session.go).
			send(cfg, "setWebsiteDataStore:", uintptr(websiteDataStore(o.DataStore)))
		}
		prefs := send(cfg, "preferences")
		send(prefs, "setValue:forKey:", uintptr(nsBool(true)), uintptr(nsString("developerExtrasEnabled")))
		// Each child has its content controller of its own, with the
		// messaging of the page and the handler of that page's messages.
		e.ucc = alloc("WKUserContentController")
		send(e.ucc, "addScriptMessageHandler:name:", uintptr(w.b.embedHandlerObject()), uintptr(nsString(embeddedMessageName)))
		addUserScript(e.ucc, embeddedPageScript, false, true)
		send(cfg, "setUserContentController:", uintptr(e.ucc))

		e.web = msgInitRectID(send(class("WKWebView"), "alloc"), sel("initWithFrame:configuration:"), NSRect{}, cfg)
		release(cfg)
		send(e.web, "setAutoresizingMask:", 0)
		send(e.web, "setNavigationDelegate:", uintptr(w.b.embedHandlerObject()))
		if o.UserAgent != "" {
			send(e.web, "setCustomUserAgent:", uintptr(nsString(o.UserAgent)))
		}
		// The clip view cuts the child to the hole; a page that paints
		// past its bounds (a fixed element, a scrollbar) stays inside.
		e.clip = msgInitRect(send(class("NSView"), "alloc"), sel("initWithFrame:"), NSRect{})
		send(e.clip, "setWantsLayer:", 1)
		send(send(e.clip, "layer"), "setMasksToBounds:", 1)
		send(e.clip, "setAutoresizingMask:", 0)
		send(e.clip, "setHidden:", 1)
		send(e.clip, "addSubview:", uintptr(e.web))
		// Behind the page, which stays on top: the page's dialog and
		// backdrop must paint over the children.
		send(w.view, "addSubview:positioned:relativeTo:", uintptr(e.clip), nsWindowBelow, uintptr(w.web))
		w.b.byEmbedWeb[e.web] = e
	})
	w.embeds = append(w.embeds, e)
	if o.URL != "" {
		e.LoadURL(o.URL)
	}
	return e, nil
}

// LoadURL loads rawURL in the child.
func (e *embedded) LoadURL(rawURL string) {
	if e == nil || e.closed || rawURL == "" {
		return
	}
	withPool(func() {
		u := nsURL(rawURL)
		if u == 0 {
			log.Printf("mygo: embedded web view: invalid URL %q", rawURL)
			return
		}
		send(e.web, "loadRequest:", uintptr(send(class("NSURLRequest"), "requestWithURL:", uintptr(u))))
	})
}

// SetBounds places the child at r, a rectangle in the page's viewport in
// CSS pixels, as Element.getBoundingClientRect reports it: the origin is
// the top-left corner of the page's viewport. The page reports it again
// when it scrolls, since getBoundingClientRect follows the scroll.
func (e *embedded) SetBounds(r platform.Rect) {
	if e == nil || e.closed || e.host == nil || e.host.closed {
		return
	}
	withPool(func() {
		if r.Width <= 0 || r.Height <= 0 {
			e.box = NSRect{}
			if !sendBool(e.clip, "isHidden") {
				send(e.clip, "setHidden:", 1)
			}
			return
		}
		e.box = contentBox(msgRect(e.host.web, sel("frame")), r)
		msgSetRect(e.clip, sel("setFrame:"), e.box)
		msgSetRect(e.web, sel("setFrame:"), NSRect{Size: e.box.Size})
		if sendBool(e.clip, "isHidden") {
			send(e.clip, "setHidden:", 0)
		}
	})
}

// contentBox converts r, a rectangle in the page's viewport in CSS pixels
// whose origin is the page's top-left corner, into the content view's
// coordinates, whose origin is its bottom-left corner: frame is the host
// web view's frame there, the page's viewport.
func contentBox(frame NSRect, r platform.Rect) NSRect {
	return NSRect{
		Origin: NSPoint{
			X: frame.Origin.X + float64(r.X),
			Y: frame.Origin.Y + frame.Size.Height - float64(r.Y) - float64(r.Height),
		},
		Size: NSSize{Width: float64(r.Width), Height: float64(r.Height)},
	}
}

// SetInputEnabled tells whether the hole takes the pointer: false while
// the page covers it, so the page keeps the clicks that dismiss the
// dialog.
func (e *embedded) SetInputEnabled(v bool) {
	if e == nil || e.closed {
		return
	}
	e.input = v
}

// CallAsyncFunction runs body as the body of an async function in the
// child's main frame (Page.CallAsyncFunction).
func (e *embedded) CallAsyncFunction(body string, cb func(string, error)) {
	if e == nil || e.closed || e.web == 0 {
		cb("", errEmbeddedClosed)
		return
	}
	callAsyncFunction(e.web, body, cb)
}

// PostMessage delivers msg to the child's page as the detail of a
// mygo:message CustomEvent on its window.
func (e *embedded) PostMessage(msg string) {
	if e == nil || e.closed || e.web == 0 {
		return
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	js := "window.dispatchEvent(new CustomEvent('" + embeddedEventName + "', { detail: " + string(data) + " }))"
	withPool(func() {
		send(e.web, "evaluateJavaScript:completionHandler:", uintptr(nsString(js)), 0)
	})
}

// OnMessage sets the callback for the strings the page posts with
// window.mygoEmbedded.postMessage. It runs on the main thread.
func (e *embedded) OnMessage(fn func(string)) {
	if e != nil && !e.closed {
		e.onMessage = fn
	}
}

// OnDidLoad sets the callback for a navigation of the child's main frame
// that finished. It runs on the main thread.
func (e *embedded) OnDidLoad(fn func(url string)) {
	if e != nil && !e.closed {
		e.onDidLoad = fn
	}
}

// OnLoadFailed sets the callback for a navigation of the child's main
// frame that failed. It runs on the main thread.
func (e *embedded) OnLoadFailed(fn func(error)) {
	if e != nil && !e.closed {
		e.onLoadFailed = fn
	}
}

// Close removes the child from the window.
func (e *embedded) Close() { e.close() }

func (e *embedded) close() {
	if e == nil || e.closed {
		return
	}
	e.closed = true
	e.box = NSRect{}
	withPool(func() {
		if e.web != 0 {
			send(e.web, "stopLoading")
			send(e.web, "setNavigationDelegate:", 0)
		}
		if e.ucc != 0 {
			send(e.ucc, "removeScriptMessageHandlerForName:", uintptr(nsString(embeddedMessageName)))
		}
		if e.clip != 0 {
			send(e.clip, "removeFromSuperview")
			release(e.clip)
			e.clip = 0
		}
		if e.web != 0 {
			if e.host != nil {
				delete(e.host.b.byEmbedWeb, e.web)
			}
			release(e.web)
			e.web = 0
		}
		if e.ucc != 0 {
			release(e.ucc)
			e.ucc = 0
		}
	})
	e.onMessage, e.onDidLoad, e.onLoadFailed = nil, nil, nil
	if e.host != nil {
		if list := e.host.embeds; len(list) > 0 {
			for i, x := range list {
				if x == e {
					e.host.embeds = append(list[:i], list[i+1:]...)
					break
				}
			}
		}
	}
}

// closeEmbeds closes the children of a window that closes (cleanup).
func (w *window) closeEmbeds() {
	embeds := w.embeds
	w.embeds = nil
	for _, e := range embeds {
		e.close()
	}
}

// holeAt reports whether the pointer at p, in the content view's
// coordinates, is over the hole while the hole takes input: the host web
// view's hitTest: then returns nil, and AppKit goes on to the views under
// it, where the child sits.
func (e *embedded) holeAt(p NSPoint) bool {
	if e.closed || !e.input || e.box.Size.Width <= 0 || e.box.Size.Height <= 0 {
		return false
	}
	return p.X >= e.box.Origin.X && p.X < e.box.Origin.X+e.box.Size.Width &&
		p.Y >= e.box.Origin.Y && p.Y < e.box.Origin.Y+e.box.Size.Height
}

// registerEmbeddedClasses registers the class the children share as their
// WKScriptMessageHandler and WKNavigationDelegate: it finds the child by
// the web view the callback is about, in the backend's table.
func registerEmbeddedClasses() {
	failed := func(self id, _ objc.SEL, web, nav, err id) {
		e := theBackend.byEmbedWeb[web]
		if e == nil || e.closed || e.onLoadFailed == nil {
			return
		}
		e.onLoadFailed(nsError(err))
	}
	classDef("MyGoEmbedHandler", "NSObject", []string{"WKScriptMessageHandler", "WKNavigationDelegate"}, []objc.MethodDef{
		method("userContentController:didReceiveScriptMessage:", func(self id, _ objc.SEL, controller, message id) {
			e := theBackend.byEmbedWeb[send(message, "webView")]
			if e == nil || e.closed || e.onMessage == nil {
				return
			}
			e.onMessage(goString(send(message, "body")))
		}),
		method("webView:didFinishNavigation:", func(self id, _ objc.SEL, web, nav id) {
			e := theBackend.byEmbedWeb[web]
			if e == nil || e.closed || e.onDidLoad == nil {
				return
			}
			e.onDidLoad(goString(send(send(web, "URL"), "absoluteString")))
		}),
		method("webView:didFailProvisionalNavigation:withError:", failed),
		method("webView:didFailNavigation:withError:", failed),
	})
}
