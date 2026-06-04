//go:build windows

package platform

import (
	"fmt"
	"strings"
)

type vkKey struct {
	vk  uint16
	ext bool // extended-key flag (arrows, nav cluster, etc.)
}

// keyTable maps documented key names (docs/02 §3.2) to virtual-key codes.
// Names are matched case-insensitively.
var keyTable = map[string]vkKey{
	"enter": {0x0D, false}, "return": {0x0D, false},
	"tab": {0x09, false}, "esc": {0x1B, false}, "escape": {0x1B, false},
	"space": {0x20, false}, "spacebar": {0x20, false},
	"backspace": {0x08, false}, "back": {0x08, false},
	"delete": {0x2E, true}, "del": {0x2E, true}, "insert": {0x2D, true}, "ins": {0x2D, true},
	"home": {0x24, true}, "end": {0x23, true},
	"pageup": {0x21, true}, "pgup": {0x21, true},
	"pagedown": {0x22, true}, "pgdn": {0x22, true},
	"up": {0x26, true}, "down": {0x28, true}, "left": {0x25, true}, "right": {0x27, true},
	"capslock": {0x14, false}, "printscreen": {0x2C, true}, "print": {0x2C, true},

	// Modifiers (also recognised by parseChord).
	"ctrl": {0x11, false}, "control": {0x11, false},
	"alt": {0x12, false}, "menu": {0x12, false},
	"shift": {0x10, false},
	"win": {0x5B, true}, "windows": {0x5B, true}, "meta": {0x5B, true}, "cmd": {0x5B, true},
}

func init() {
	// F1..F24 => 0x70..0x87
	for i := 1; i <= 24; i++ {
		keyTable[fmt.Sprintf("f%d", i)] = vkKey{uint16(0x70 + i - 1), false}
	}
}

var modifierVKs = map[string]vkKey{
	"ctrl": {0x11, false}, "control": {0x11, false},
	"alt": {0x12, false}, "menu": {0x12, false},
	"shift":   {0x10, false},
	"win":     {0x5B, true}, "windows": {0x5B, true}, "meta": {0x5B, true}, "cmd": {0x5B, true},
}

// lookupVK resolves a key name to a virtual-key code. Single printable
// characters fall back to VkKeyScanW for the active layout.
func lookupVK(name string) (uint16, bool, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return 0, false, fmt.Errorf("empty key name")
	}
	if k, ok := keyTable[n]; ok {
		return k.vk, k.ext, nil
	}
	// Single character (letter, digit, punctuation).
	runes := []rune(name)
	if len(runes) == 1 {
		r := runes[0]
		switch {
		case r >= 'a' && r <= 'z':
			return uint16(r - 'a' + 'A'), false, nil
		case r >= 'A' && r <= 'Z':
			return uint16(r), false, nil
		case r >= '0' && r <= '9':
			return uint16(r), false, nil
		}
		res, _, _ := procVkKeyScanW.Call(uintptr(uint16(r)))
		vk := uint16(res & 0xFF)
		if vk != 0 && vk != 0xFF {
			return vk, false, nil
		}
	}
	return 0, false, fmt.Errorf("unknown key %q", name)
}

// parseChord splits "Ctrl+Shift+S" into its modifiers and the main key.
func parseChord(chord string) (mods []vkKey, key vkKey, err error) {
	chord = strings.TrimSpace(chord)
	if chord == "" {
		return nil, vkKey{}, fmt.Errorf("empty key chord")
	}
	parts := splitChord(chord)
	for i, p := range parts {
		lower := strings.ToLower(strings.TrimSpace(p))
		if m, ok := modifierVKs[lower]; ok && i < len(parts)-1 {
			mods = append(mods, m)
			continue
		}
		vk, ext, e := lookupVK(p)
		if e != nil {
			return nil, vkKey{}, e
		}
		key = vkKey{vk, ext}
	}
	return mods, key, nil
}

// splitChord splits on '+' while supporting a literal trailing '+'.
func splitChord(chord string) []string {
	raw := strings.Split(chord, "+")
	var out []string
	for i, seg := range raw {
		if seg == "" {
			if i > 0 {
				out = append(out, "+") // literal plus
			}
			continue
		}
		out = append(out, seg)
	}
	if len(out) == 0 {
		return []string{chord}
	}
	return out
}
