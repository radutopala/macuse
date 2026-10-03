package core

import (
	"fmt"
	"strings"
)

// Modifier is a bitset of held modifier keys.
type Modifier uint8

const (
	ModCmd Modifier = 1 << iota
	ModCtrl
	ModAlt
	ModShift
)

// Combo is one key press with modifiers held, e.g. cmd+shift+t.
type Combo struct {
	Mods Modifier
	// Key is the canonical key name: a single lowercase character or one of
	// the names in namedKeys.
	Key string
}

var modifierNames = map[string]Modifier{
	"cmd":     ModCmd,
	"command": ModCmd,
	"meta":    ModCmd,
	"super":   ModCmd,
	"ctrl":    ModCtrl,
	"control": ModCtrl,
	"alt":     ModAlt,
	"option":  ModAlt,
	"opt":     ModAlt,
	"shift":   ModShift,
}

// keyAliases maps accepted spellings to canonical key names.
var keyAliases = map[string]string{
	"enter":      "return",
	"esc":        "escape",
	"arrowleft":  "left",
	"arrowright": "right",
	"arrowup":    "up",
	"arrowdown":  "down",
	"pgup":       "pageup",
	"pgdn":       "pagedown",
	"del":        "delete",
}

// macKeyCodes are the macOS virtual key codes (ANSI layout) for every
// canonical key name.
var macKeyCodes = map[string]uint16{
	"a": 0, "s": 1, "d": 2, "f": 3, "h": 4, "g": 5, "z": 6, "x": 7, "c": 8, "v": 9,
	"b": 11, "q": 12, "w": 13, "e": 14, "r": 15, "y": 16, "t": 17,
	"1": 18, "2": 19, "3": 20, "4": 21, "6": 22, "5": 23, "=": 24, "9": 25, "7": 26,
	"-": 27, "8": 28, "0": 29, "]": 30, "o": 31, "u": 32, "[": 33, "i": 34, "p": 35,
	"l": 37, "j": 38, "'": 39, "k": 40, ";": 41, "\\": 42, ",": 43, "/": 44,
	"n": 45, "m": 46, ".": 47, "`": 50,
	"return": 36, "tab": 48, "space": 49, "backspace": 51, "escape": 53,
	"f1": 122, "f2": 120, "f3": 99, "f4": 118, "f5": 96, "f6": 97,
	"f7": 98, "f8": 100, "f9": 101, "f10": 109, "f11": 103, "f12": 111,
	"home": 115, "pageup": 116, "delete": 117, "end": 119, "pagedown": 121,
	"left": 123, "right": 124, "down": 125, "up": 126,
}

// ParseKeys parses a whitespace-separated sequence of combos such as
// "cmd+a backspace". Names are case-insensitive.
func ParseKeys(s string) ([]Combo, error) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return nil, fmt.Errorf("no keys given")
	}
	combos := make([]Combo, 0, len(fields))
	for _, f := range fields {
		c, err := parseCombo(f)
		if err != nil {
			return nil, err
		}
		combos = append(combos, c)
	}
	return combos, nil
}

func parseCombo(s string) (Combo, error) {
	parts := strings.Split(strings.ToLower(s), "+")
	var c Combo
	for i, p := range parts {
		if i < len(parts)-1 {
			m, ok := modifierNames[p]
			if !ok {
				return Combo{}, fmt.Errorf("unknown modifier %q in %q", p, s)
			}
			c.Mods |= m
			continue
		}
		if alias, ok := keyAliases[p]; ok {
			p = alias
		}
		if _, ok := macKeyCodes[p]; !ok {
			return Combo{}, fmt.Errorf("unknown key %q in %q", p, s)
		}
		c.Key = p
	}
	return c, nil
}

// MacKeyCode returns the macOS virtual key code for a canonical key name.
func MacKeyCode(key string) (uint16, bool) {
	code, ok := macKeyCodes[key]
	return code, ok
}
