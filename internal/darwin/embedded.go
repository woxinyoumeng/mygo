//go:build darwin

package darwin

// Page-embedded web views: a child WKWebView behind the page of a
// window. The page leaves a transparent hole where the child shows
// through, the host web view's hitTest: hands the pointer in that hole to
// the child unless the page says a dialog covers it, and the child is
// clipped to the hole. This is the first macOS cut of the layout Electron
// apps use for <webview>: the page places the child, its own dialog and
// backdrop paint over it, and the native side only converts the page's
// viewport rectangle to the content view's coordinates.

import (
	"errors"
	"log"

	"github.com/egoist/mygo/internal/platform"
)

// embedded is a child web view behind the page of window host.
type embedded struct {
	host *window
	web  id
	// clip holds web, cut to the hole.
	clip id
	// box is the hole in the content view's coordinates, whose origin is
	// the bottom-left corner. Its size is zero while the page shows no
	// hole.
	box NSRect
	// input tells whether the hole takes the pointer; the page clears it
	// while a dialog or backdrop covers the hole.
	input  bool
	closed bool
}

var (
	errEmbeddedClosed      = errors.New("mygo: the window is closed")
	errEmbeddedNoPage      = errors.New("mygo: the window shows native content, not a web page")
	errEmbeddedTwice       = errors.New("mygo: the window already has an embedded web view")
	errEmbeddedUnsupported = errors.New("mygo: this macOS version cannot make a page transparent (_setDrawsBackground: is missing)")
)

// NewEmbeddedWebView creates a child web view behind w's page.
func (w *window) NewEmbeddedWebView(o platform.EmbeddedWebViewOptions) (platform.EmbeddedWebView, error) {
	if w.closed {
		return nil, errEmbeddedClosed
	}
	if w.web == 0 || w.view == 0 {
		// A window showing Content has no page to put the hole in.
		return nil, errEmbeddedNoPage
	}
	if w.embedded != nil {
		return nil, errEmbeddedTwice
	}
	// The layout needs a transparent page: where the page paints nothing,
	// the child behind it must show. Without the private setter the page
	// would sit opaque over the child, and nothing would show through.
	if !respondsTo(w.web, "_setDrawsBackground:") {
		return nil, errEmbeddedUnsupported
	}
	e := &embedded{host: w, input: true}
	withPool(func() {
		send(w.web, "_setDrawsBackground:", 0)
		send(w.web, "setValue:forKey:", uintptr(nsBool(false)), uintptr(nsString("drawsBackground")))
		if respondsTo(w.web, "setUnderPageBackgroundColor:") {
			send(w.web, "setUnderPageBackgroundColor:", uintptr(send(class("NSColor"), "clearColor")))
		}

		cfg := alloc("WKWebViewConfiguration")
		prefs := send(cfg, "preferences")
		send(prefs, "setValue:forKey:", uintptr(nsBool(true)), uintptr(nsString("developerExtrasEnabled")))
		e.web = msgInitRectID(send(class("WKWebView"), "alloc"), sel("initWithFrame:configuration:"), NSRect{}, cfg)
		release(cfg)
		send(e.web, "setAutoresizingMask:", 0)
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
		// backdrop must paint over the child.
		send(w.view, "addSubview:positioned:relativeTo:", uintptr(e.clip), nsWindowBelow, uintptr(w.web))
	})
	w.embedded = e
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
		box := e.box
		msgSetRect(e.clip, sel("setFrame:"), box)
		msgSetRect(e.web, sel("setFrame:"), NSRect{Size: box.Size})
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
		}
		if e.clip != 0 {
			send(e.clip, "removeFromSuperview")
			release(e.clip)
			e.clip = 0
		}
		if e.web != 0 {
			release(e.web)
			e.web = 0
		}
	})
	if e.host != nil {
		if e.host.embedded == e {
			e.host.embedded = nil
		}
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
