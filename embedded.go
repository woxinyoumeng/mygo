package mygo

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/egoist/mygo/internal/platform"
)

// EmbeddedWebViewOptions configures Window.NewEmbeddedWebView.
type EmbeddedWebViewOptions struct {
	// URL loads in the child right after it is created.
	URL string
	// UserAgent overrides the child's user agent, which WebKit otherwise
	// shares with the host page.
	UserAgent string
	// Session is the session of the child's page, as PageOptions.Session
	// is the host page's: its cookies and storage, apart from the other
	// sessions' data. Nil inherits the host page's session.
	Session *Session
}

var (
	// errEmbeddedUnsupported is returned where the backend cannot embed a
	// web view: only the macOS backend implements it for now.
	errEmbeddedUnsupported = errors.New("mygo: embedded web views are not supported on this platform")
	errEmbeddedClosed      = errors.New("mygo: the embedded web view has been closed")
)

// EmbeddedWebView is a web view behind the page of a window. The page
// leaves it a transparent hole and reports the hole's rectangle and
// whether a dialog covers it, with anything the app polls the page with
// (Page.Eval); the native side places the child, and hands it the pointer
// in the hole while the hole takes input. A page's dialog or backdrop then
// paints over it, as it paints over the rest of the page.
//
// A window can have several children, each with its hole, as a page with
// several <webview> elements would. This is the macOS version: the child
// does not scroll or zoom with the page, and the page places it by calling
// SetBounds with the hole's viewport rectangle.
type EmbeddedWebView struct {
	host   *Window
	native platform.EmbeddedWebView
	// session is the session the child's page uses, attached while the
	// child lives.
	session    *Session
	detachOnce sync.Once
	closed     atomic.Bool
	// mu guards the callbacks, which run on the main thread but are set
	// from any.
	mu           sync.Mutex
	onMessage    func(string)
	onDidLoad    func(url string)
	onLoadFailed func(error)
}

// NewEmbeddedWebView creates a web view behind the page of w. w must show
// a page (not Content); call it after the page is ready, so that the
// page's first bounds follow. The page places the child with SetBounds, and
// makes it transparent to the pointer with SetInputEnabled while a dialog
// covers it. On macOS the page is made transparent for the child to show
// through; on platforms without support it returns an error.
func (w *Window) NewEmbeddedWebView(opts EmbeddedWebViewOptions) (*EmbeddedWebView, error) {
	var (
		ev  *EmbeddedWebView
		err error
	)
	onMain(func() {
		if w.native == nil {
			err = errDestroyed
			return
		}
		viewer, ok := w.native.(platform.EmbeddedWebViewer)
		if !ok {
			err = errEmbeddedUnsupported
			return
		}
		session := opts.Session
		if session == nil {
			session = w.pageSession()
		}
		store, serr := session.attach()
		if serr != nil {
			err = serr
			return
		}
		n, e := viewer.NewEmbeddedWebView(platform.EmbeddedWebViewOptions{
			URL:       opts.URL,
			UserAgent: opts.UserAgent,
			DataStore: store,
		})
		if e != nil {
			session.detach()
			err = e
			return
		}
		ev = &EmbeddedWebView{host: w, native: n, session: session}
		n.OnMessage(ev.deliverMessage)
		n.OnDidLoad(ev.deliverDidLoad)
		n.OnLoadFailed(ev.deliverLoadFailed)
		registerEmbedded(ev)
	})
	return ev, err
}

// SetBounds places the child at r, a rectangle in the page's viewport in
// CSS pixels whose origin is the page's top-left corner, as
// Element.getBoundingClientRect reports it. A rectangle without a size
// hides the child. The page's zoom factor and scrolling are not taken into
// account: this version places the child for an unscrolled page at zoom 1,
// and the page reports the rectangle again when it scrolls.
func (e *EmbeddedWebView) SetBounds(r Rectangle) {
	if e == nil || e.native == nil || e.closed.Load() {
		return
	}
	onMain(func() {
		if e.closed.Load() || e.host.native == nil {
			return
		}
		e.native.SetBounds(platform.Rect(r))
	})
}

// SetInputEnabled tells whether the hole takes the pointer. Pass false
// while the page opens a dialog or another overlay that covers the hole:
// the page then gets the clicks, and its backdrop paints over the child.
func (e *EmbeddedWebView) SetInputEnabled(v bool) {
	if e == nil || e.native == nil || e.closed.Load() {
		return
	}
	onMain(func() {
		if e.closed.Load() || e.host.native == nil {
			return
		}
		e.native.SetInputEnabled(v)
	})
}

// LoadURL loads url in the child.
func (e *EmbeddedWebView) LoadURL(url string) {
	if e == nil || e.native == nil || e.closed.Load() {
		return
	}
	onMain(func() {
		if e.closed.Load() || e.host.native == nil {
			return
		}
		e.native.LoadURL(url)
	})
}

// Eval evaluates JavaScript in the child's page and returns its result,
// decoded from JSON, as Page.Eval does for the window's page.
func (e *EmbeddedWebView) Eval(code string) (any, error) {
	return e.EvalContext(context.Background(), code)
}

// EvalContext is Eval with a context that can abort the wait.
func (e *EmbeddedWebView) EvalContext(ctx context.Context, code string) (any, error) {
	raw, err := e.eval(ctx, code)
	if err != nil || len(raw) == 0 {
		return nil, err
	}
	var v any
	if err := decodeResult(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}

func (e *EmbeddedWebView) eval(ctx context.Context, code string) (jsontext.Value, error) {
	if e == nil || e.native == nil {
		return nil, errEmbeddedClosed
	}
	return evalIn(ctx, nil, code, func(body string, cb func(string, error)) error {
		ran := false
		onMain(func() {
			if e.closed.Load() {
				return
			}
			ran = true
			e.native.CallAsyncFunction(body, cb)
		})
		if !ran {
			return errEmbeddedClosed
		}
		return nil
	})
}

// OnMessage sets the callback for the strings the child's page posts with
// window.mygoEmbedded.postMessage. It runs on the main thread; nil removes
// it.
func (e *EmbeddedWebView) OnMessage(fn func(string)) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.onMessage = fn
	e.mu.Unlock()
}

// PostMessage posts msg to the child's page, which hears it as a
// mygo:message CustomEvent on its window, with msg as event.detail, and
// with window.mygoEmbedded.onMessage. The page gets only the messages
// posted after its document started.
func (e *EmbeddedWebView) PostMessage(msg string) {
	if e == nil || e.native == nil || e.closed.Load() {
		return
	}
	onMain(func() {
		if e.closed.Load() || e.host.native == nil {
			return
		}
		e.native.PostMessage(msg)
	})
}

// OnDidLoad sets the callback for a main-frame navigation of the child
// that finished, with the URL it loaded. It runs on the main thread; nil
// removes it.
func (e *EmbeddedWebView) OnDidLoad(fn func(url string)) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.onDidLoad = fn
	e.mu.Unlock()
}

// OnLoadFailed sets the callback for a main-frame navigation of the child
// that failed. It runs on the main thread; nil removes it.
func (e *EmbeddedWebView) OnLoadFailed(fn func(error)) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.onLoadFailed = fn
	e.mu.Unlock()
}

func (e *EmbeddedWebView) deliverMessage(msg string) {
	e.mu.Lock()
	fn := e.onMessage
	e.mu.Unlock()
	if fn != nil {
		fn(msg)
	}
}

func (e *EmbeddedWebView) deliverDidLoad(url string) {
	e.mu.Lock()
	fn := e.onDidLoad
	e.mu.Unlock()
	if fn != nil {
		fn(url)
	}
}

func (e *EmbeddedWebView) deliverLoadFailed(err error) {
	e.mu.Lock()
	fn := e.onLoadFailed
	e.mu.Unlock()
	if fn != nil {
		fn(err)
	}
}

// Close removes the child from the window. The host page keeps the
// transparent background it got for the child; a page that no longer shows
// a hole paints its own background over it.
func (e *EmbeddedWebView) Close() {
	if e == nil || !e.closed.CompareAndSwap(false, true) {
		return
	}
	onMain(func() {
		if e.native != nil {
			e.native.Close()
		}
		e.detach()
	})
}

// Window returns the window whose page hosts the child.
func (e *EmbeddedWebView) Window() *Window { return e.host }

// detach lets the child's session go, once.
func (e *EmbeddedWebView) detach() {
	e.detachOnce.Do(func() {
		if e.session != nil {
			e.session.detach()
		}
		unregisterEmbedded(e)
	})
}

// embeddedViews are the children of each window, so that a window that
// closes without Close on its children lets their sessions go too. The
// lock covers the calls from other goroutines; the map is edited on the
// main thread otherwise.
var embeddedViews = struct {
	sync.Mutex
	m map[*Window][]*EmbeddedWebView
}{m: map[*Window][]*EmbeddedWebView{}}

func registerEmbedded(e *EmbeddedWebView) {
	embeddedViews.Lock()
	embeddedViews.m[e.host] = append(embeddedViews.m[e.host], e)
	embeddedViews.Unlock()
}

func unregisterEmbedded(e *EmbeddedWebView) {
	embeddedViews.Lock()
	defer embeddedViews.Unlock()
	list := embeddedViews.m[e.host]
	for i, x := range list {
		if x == e {
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(embeddedViews.m, e.host)
		return
	}
	embeddedViews.m[e.host] = list
}

// closeEmbeddedViews lets the sessions of a window's children go; the
// backend closed the children before the window told it was closing.
func closeEmbeddedViews(w *Window) {
	embeddedViews.Lock()
	list := embeddedViews.m[w]
	delete(embeddedViews.m, w)
	embeddedViews.Unlock()
	for _, e := range list {
		e.closed.Store(true)
		e.detach()
	}
}
