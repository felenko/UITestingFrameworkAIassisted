//go:build windows

package platform

import (
	"fmt"
	"syscall"
	"time"
)

func (d *winDriver) setCursor(p Point) error {
	r, _, err := procSetCursorPos.Call(uintptr(int32(p.X)), uintptr(int32(p.Y)))
	if r == 0 {
		// SetCursorPos returns 0 (failure) when the coordinate can't be reached —
		// most often because it resolves OUTSIDE the visible virtual desktop
		// (e.g. a window-relative point below/right of the screen). In that case
		// GetLastError is frequently 0, which would render as the misleading
		// "The operation completed successfully"; report the rejected point and
		// the virtual-screen bounds so the cause is obvious.
		if errno, ok := err.(syscall.Errno); ok && errno != 0 {
			return fmt.Errorf("SetCursorPos(%d,%d): %w", p.X, p.Y, err)
		}
		vx, vy, vw, vh := virtualScreenRect()
		return fmt.Errorf("SetCursorPos(%d,%d) rejected: point is outside the visible desktop [%d,%d %dx%d] (window-relative coordinate likely resolves off-screen)",
			p.X, p.Y, vx, vy, vw, vh)
	}
	return nil
}

// virtualScreenRect returns the bounding rectangle of all monitors (the area
// SetCursorPos accepts), in physical pixels.
func virtualScreenRect() (x, y, w, h int) {
	rx, _, _ := procGetSystemMetrics.Call(smXVirtualScreen)
	ry, _, _ := procGetSystemMetrics.Call(smYVirtualScreen)
	rw, _, _ := procGetSystemMetrics.Call(smCXVirtualScreen)
	rh, _, _ := procGetSystemMetrics.Call(smCYVirtualScreen)
	return int(int32(rx)), int(int32(ry)), int(int32(rw)), int(int32(rh))
}

func (d *winDriver) MouseMove(p Point) error { return d.setCursor(p) }

func buttonFlags(button string) (down, up uint32, err error) {
	switch button {
	case "", "left":
		return mouseeventfLeftDown, mouseeventfLeftUp, nil
	case "right":
		return mouseeventfRightDown, mouseeventfRightUp, nil
	case "middle":
		return mouseeventfMiddleDown, mouseeventfMiddleUp, nil
	default:
		return 0, 0, fmt.Errorf("unknown mouse button %q", button)
	}
}

func (d *winDriver) MouseClick(p Point, button string, count int) error {
	if count < 1 {
		count = 1
	}
	if err := d.setCursor(p); err != nil {
		return err
	}
	down, up, err := buttonFlags(button)
	if err != nil {
		return err
	}
	for i := 0; i < count; i++ {
		if err := sendInputs([]input{
			makeMouseInput(mouseInput{dwFlags: down}),
			makeMouseInput(mouseInput{dwFlags: up}),
		}); err != nil {
			return err
		}
		if count > 1 {
			time.Sleep(40 * time.Millisecond)
		}
	}
	return nil
}

func (d *winDriver) MouseDown(p Point, button string) error {
	if err := d.setCursor(p); err != nil {
		return err
	}
	down, _, err := buttonFlags(button)
	if err != nil {
		return err
	}
	return sendInputs([]input{makeMouseInput(mouseInput{dwFlags: down})})
}

func (d *winDriver) MouseUp(p Point, button string) error {
	if err := d.setCursor(p); err != nil {
		return err
	}
	_, up, err := buttonFlags(button)
	if err != nil {
		return err
	}
	return sendInputs([]input{makeMouseInput(mouseInput{dwFlags: up})})
}

func (d *winDriver) MouseDrag(from, to Point, button string) error {
	down, up, err := buttonFlags(button)
	if err != nil {
		return err
	}
	if err := d.setCursor(from); err != nil {
		return err
	}
	time.Sleep(20 * time.Millisecond)
	if err := sendInputs([]input{makeMouseInput(mouseInput{dwFlags: down})}); err != nil {
		return err
	}
	// Move in a few steps so the target registers a real drag.
	const steps = 10
	for i := 1; i <= steps; i++ {
		x := from.X + (to.X-from.X)*i/steps
		y := from.Y + (to.Y-from.Y)*i/steps
		if err := d.setCursor(Point{X: x, Y: y}); err != nil {
			return err
		}
		time.Sleep(15 * time.Millisecond)
	}
	return sendInputs([]input{makeMouseInput(mouseInput{dwFlags: up})})
}

func (d *winDriver) MouseScroll(p Point, dx, dy int) error {
	if err := d.setCursor(p); err != nil {
		return err
	}
	var inputs []input
	if dy != 0 {
		// Spec: positive dy scrolls down. Win32 wheel: positive = up.
		inputs = append(inputs, makeMouseInput(mouseInput{
			dwFlags:   mouseeventfWheel,
			mouseData: uint32(int32(-dy * wheelDelta)),
		}))
	}
	if dx != 0 {
		inputs = append(inputs, makeMouseInput(mouseInput{
			dwFlags:   mouseeventfHWheel,
			mouseData: uint32(int32(dx * wheelDelta)),
		}))
	}
	return sendInputs(inputs)
}

// TypeText sends each rune as a Unicode key event, robust across layouts.
func (d *winDriver) TypeText(text string, perChar time.Duration) error {
	for _, r := range text {
		for _, unit := range utf16Units(r) {
			if err := sendInputs([]input{
				makeKeybdInput(keybdInput{wScan: unit, dwFlags: keyeventfUnicode}),
				makeKeybdInput(keybdInput{wScan: unit, dwFlags: keyeventfUnicode | keyeventfKeyUp}),
			}); err != nil {
				return err
			}
		}
		if perChar > 0 {
			time.Sleep(perChar)
		}
	}
	return nil
}

// utf16Units splits a rune into UTF-16 code units (handles astral planes).
func utf16Units(r rune) []uint16 {
	if r <= 0xFFFF {
		return []uint16{uint16(r)}
	}
	r -= 0x10000
	return []uint16{
		uint16(0xD800 + (r >> 10)),
		uint16(0xDC00 + (r & 0x3FF)),
	}
}

func (d *winDriver) KeyDown(key string) error {
	vk, ext, err := lookupVK(key)
	if err != nil {
		return err
	}
	return sendInputs([]input{keyEvent(vk, ext, false)})
}

func (d *winDriver) KeyUp(key string) error {
	vk, ext, err := lookupVK(key)
	if err != nil {
		return err
	}
	return sendInputs([]input{keyEvent(vk, ext, true)})
}

// KeyPress presses a chord like "Ctrl+Shift+S": modifiers down, key down/up,
// modifiers up (in reverse).
func (d *winDriver) KeyPress(chord string) error {
	mods, key, err := parseChord(chord)
	if err != nil {
		return err
	}
	var seq []input
	for _, m := range mods {
		seq = append(seq, keyEvent(m.vk, m.ext, false))
	}
	if key.vk != 0 {
		seq = append(seq, keyEvent(key.vk, key.ext, false))
		seq = append(seq, keyEvent(key.vk, key.ext, true))
	}
	for i := len(mods) - 1; i >= 0; i-- {
		seq = append(seq, keyEvent(mods[i].vk, mods[i].ext, true))
	}
	return sendInputs(seq)
}

func keyEvent(vk uint16, ext bool, up bool) input {
	var flags uint32
	if ext {
		flags |= keyeventfExtended
	}
	if up {
		flags |= keyeventfKeyUp
	}
	return makeKeybdInput(keybdInput{wVk: vk, dwFlags: flags})
}
