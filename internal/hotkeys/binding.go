package hotkeys

import (
	"fmt"
	"strconv"
	"strings"
)

// Modifier values intentionally match Win32's MOD_* constants. Keeping the
// parser platform-neutral lets config validation catch a typo on Linux before a
// Windows release ever sees it.
const (
	ModAlt     uint32 = 0x0001
	ModControl uint32 = 0x0002
	ModShift   uint32 = 0x0004
	ModWin     uint32 = 0x0008
)

// Binding is one RegisterHotKey-compatible keyboard chord.
type Binding struct {
	Modifiers  uint32
	VirtualKey uint32
	Key        string
}

// String returns the canonical spelling used for duplicate detection and logs.
func (b Binding) String() string {
	var parts []string
	if b.Modifiers&ModControl != 0 {
		parts = append(parts, "Ctrl")
	}
	if b.Modifiers&ModAlt != 0 {
		parts = append(parts, "Alt")
	}
	if b.Modifiers&ModShift != 0 {
		parts = append(parts, "Shift")
	}
	if b.Modifiers&ModWin != 0 {
		parts = append(parts, "Win")
	}
	return strings.Join(append(parts, b.Key), "+")
}

// ParseBinding accepts chords such as "V", "Ctrl+Shift+M" and "Alt+F8".
// The key names are layout-independent Win32 virtual keys, not characters typed
// through the current keyboard layout.
func ParseBinding(text string) (Binding, error) {
	raw := strings.TrimSpace(text)
	if raw == "" {
		return Binding{}, fmt.Errorf("binding is empty")
	}

	parts := strings.Split(raw, "+")
	var binding Binding
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return Binding{}, fmt.Errorf("%q contains an empty key name", raw)
		}
		last := i == len(parts)-1
		if modifier, ok := parseModifier(part); ok {
			if last {
				return Binding{}, fmt.Errorf("%q ends with a modifier instead of a key", raw)
			}
			if binding.Modifiers&modifier != 0 {
				return Binding{}, fmt.Errorf("%q repeats modifier %q", raw, part)
			}
			binding.Modifiers |= modifier
			continue
		}
		if !last {
			return Binding{}, fmt.Errorf("%q has non-modifier %q before the final key", raw, part)
		}
		key, vk, ok := parseVirtualKey(part)
		if !ok {
			return Binding{}, fmt.Errorf("%q has unsupported key %q", raw, part)
		}
		binding.Key = key
		binding.VirtualKey = vk
	}
	if binding.Key == "" {
		return Binding{}, fmt.Errorf("%q has no key", raw)
	}
	return binding, nil
}

func parseModifier(part string) (uint32, bool) {
	switch strings.ToLower(part) {
	case "alt":
		return ModAlt, true
	case "ctrl", "control":
		return ModControl, true
	case "shift":
		return ModShift, true
	case "win", "windows", "super":
		return ModWin, true
	default:
		return 0, false
	}
}

func parseVirtualKey(part string) (string, uint32, bool) {
	upper := strings.ToUpper(part)
	if len(upper) == 1 {
		key := upper[0]
		if key >= 'A' && key <= 'Z' || key >= '0' && key <= '9' {
			return string(key), uint32(key), true
		}
	}
	if strings.HasPrefix(upper, "F") {
		number, err := strconv.Atoi(strings.TrimPrefix(upper, "F"))
		if err == nil && number >= 1 && number <= 24 {
			return fmt.Sprintf("F%d", number), uint32(0x70 + number - 1), true
		}
	}

	type namedKey struct {
		name string
		vk   uint32
	}
	named := map[string]namedKey{
		"BACKTICK": {"Grave", 0xc0},
		"DELETE":   {"Delete", 0x2e},
		"DOWN":     {"Down", 0x28},
		"END":      {"End", 0x23},
		"ENTER":    {"Enter", 0x0d},
		"ESC":      {"Escape", 0x1b},
		"ESCAPE":   {"Escape", 0x1b},
		"GRAVE":    {"Grave", 0xc0},
		"HOME":     {"Home", 0x24},
		"INSERT":   {"Insert", 0x2d},
		"LEFT":     {"Left", 0x25},
		"PAGEDOWN": {"PageDown", 0x22},
		"PAGEUP":   {"PageUp", 0x21},
		"PGDN":     {"PageDown", 0x22},
		"PGUP":     {"PageUp", 0x21},
		"RETURN":   {"Enter", 0x0d},
		"RIGHT":    {"Right", 0x27},
		"SPACE":    {"Space", 0x20},
		"TAB":      {"Tab", 0x09},
		"UP":       {"Up", 0x26},
	}
	key, ok := named[upper]
	return key.name, key.vk, ok
}
