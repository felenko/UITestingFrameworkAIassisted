package runner

import (
	"image"
	"image/color"
	"testing"

	"github.com/felenko/uitest/internal/core/session"
)

func solid(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

func TestPixelChanged(t *testing.T) {
	black := color.RGBA{0, 0, 0, 255}
	white := color.RGBA{255, 255, 255, 255}

	a := solid(100, 100, black)
	b := solid(100, 100, black)
	if pixelChanged(a, b, changedTol) {
		t.Error("identical images should not be 'changed'")
	}

	c := solid(100, 100, white)
	if !pixelChanged(a, c, changedTol) {
		t.Error("fully different images should be 'changed'")
	}

	// Differing dimensions always count as changed.
	if !pixelChanged(a, solid(120, 100, black), changedTol) {
		t.Error("different dimensions should be 'changed'")
	}

	// A tiny localized change should fall under the tolerance (not 'changed').
	d := solid(100, 100, black)
	for y := 0; y < 3; y++ {
		for x := 0; x < 3; x++ {
			d.Set(x, y, white)
		}
	}
	if pixelChanged(a, d, changedTol) {
		t.Error("a tiny change should be below the changed tolerance")
	}
}

func TestIsActuation(t *testing.T) {
	for _, a := range []string{"mouse_click", "type_text", "focus_window", "mouse_drag"} {
		if !isActuation(a) {
			t.Errorf("%s should be actuation", a)
		}
	}
	for _, a := range []string{"screenshot", "assert_ai", "read_text_ai", "wait", "launch_app"} {
		if isActuation(a) {
			t.Errorf("%s should NOT be actuation", a)
		}
	}
}

func TestDefaultVerify(t *testing.T) {
	// Click/drag/scroll get an auto changed-verify; typing/keys do not (avoid
	// duplicating input on retry).
	if v := defaultVerify(&session.Command{Action: "mouse_click"}); v == nil || !v.Changed {
		t.Error("mouse_click should default to a changed-verify")
	}
	if v := defaultVerify(&session.Command{Action: "type_text"}); v != nil {
		t.Error("type_text must not auto-retry (would duplicate input)")
	}
	if v := defaultVerify(&session.Command{Action: "key_press"}); v != nil {
		t.Error("key_press must not auto-retry")
	}
}
