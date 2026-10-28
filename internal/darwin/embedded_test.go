package darwin

import (
	"testing"

	"github.com/egoist/mygo/internal/platform"
)

func TestContentBox(t *testing.T) {
	frame := NSRect{Size: NSSize{Width: 960, Height: 648}}
	got := contentBox(frame, platform.Rect{X: 20, Y: 120, Width: 600, Height: 320})
	want := NSRect{Origin: NSPoint{X: 20, Y: 208}, Size: NSSize{Width: 600, Height: 320}}
	if got != want {
		t.Fatalf("contentBox = %+v, want %+v", got, want)
	}
	// A frame that does not start at the content view's origin, as with a
	// docked inspector, moves the box with it.
	frame = NSRect{Origin: NSPoint{X: 10, Y: 40}, Size: NSSize{Width: 800, Height: 600}}
	got = contentBox(frame, platform.Rect{X: 0, Y: 0, Width: 100, Height: 50})
	want = NSRect{Origin: NSPoint{X: 10, Y: 590}, Size: NSSize{Width: 100, Height: 50}}
	if got != want {
		t.Fatalf("contentBox with offset frame = %+v, want %+v", got, want)
	}
}

func TestHoleAt(t *testing.T) {
	e := &embedded{input: true, box: NSRect{Origin: NSPoint{X: 20, Y: 208}, Size: NSSize{Width: 600, Height: 320}}}
	inside := []NSPoint{{X: 20, Y: 208}, {X: 320, Y: 314}, {X: 619, Y: 527}}
	for _, p := range inside {
		if !e.holeAt(p) {
			t.Errorf("holeAt(%v) = false, want true", p)
		}
	}
	outside := []NSPoint{{X: 19, Y: 314}, {X: 320, Y: 207}, {X: 620, Y: 314}, {X: 320, Y: 528}}
	for _, p := range outside {
		if e.holeAt(p) {
			t.Errorf("holeAt(%v) = true, want false", p)
		}
	}
	// A page that covers the hole keeps the pointer.
	e.input = false
	if e.holeAt(NSPoint{X: 320, Y: 314}) {
		t.Error("holeAt with input disabled = true, want false")
	}
	// No box (the page shows no hole) takes nothing.
	e.input = true
	e.box = NSRect{}
	if e.holeAt(NSPoint{X: 320, Y: 314}) {
		t.Error("holeAt without a box = true, want false")
	}
}
