package mygo

import (
	"errors"
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
}

// errEmbeddedUnsupported is returned where the backend cannot embed a web
// view: only the macOS backend implements it for now.
var errEmbeddedUnsupported = errors.New("mygo: embedded web views are not supported on this platform")

// EmbeddedWebView is a web view behind the page of a window. The page
// leaves it a transparent hole and reports the hole's rectangle and
// whether a dialog covers it, with anything the app polls the page with
// (Page.Eval); the native side places the child, and hands it the pointer
// in the hole while the hole takes input. A page's dialog or backdrop then
// paints over it, as it paints over the rest of the page.
//
// This is the macOS first cut: the child does not scroll with the page,
// and the page places it by calling SetBounds with the hole's viewport
// rectangle.
type EmbeddedWebView struct {
	host   *Window
	native platform.EmbeddedWebView
	closed atomic.Bool
}

// NewEmbeddedWebView creates a web view behind the page of w. w must show
// a page (not Content) and have no embedded web view yet: this version
// gives each window one child. Call it after the page is ready, so that the
// page's first bounds follow. On macOS the page is made transparent for
// the child to show through; on platforms without support it returns an
// error.
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
		n, e := viewer.NewEmbeddedWebView(platform.EmbeddedWebViewOptions{
			URL:       opts.URL,
			UserAgent: opts.UserAgent,
		})
		if e != nil {
			err = e
			return
		}
		ev = &EmbeddedWebView{host: w, native: n}
	})
	return ev, err
}

// SetBounds places the child at r, a rectangle in the page's viewport in
// CSS pixels whose origin is the page's top-left corner, as
// Element.getBoundingClientRect reports it. A rectangle without a size
// hides the child. The page's zoom factor and scrolling are not taken into
// account: this version places the child for an unscrolled page at zoom 1.
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

// Close removes the child from the window. The host page keeps the
// transparent background it got for the child; a page that no longer shows
// a hole paints its own background over it.
func (e *EmbeddedWebView) Close() {
	if e == nil || e.native == nil || !e.closed.CompareAndSwap(false, true) {
		return
	}
	onMain(func() { e.native.Close() })
}

// Window returns the window whose page hosts the child.
func (e *EmbeddedWebView) Window() *Window { return e.host }
