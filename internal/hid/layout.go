package hid

import (
	"fmt"
	"strings"
)

// USB HID modifier bits (firmware ui/src/keyboardMappings.ts `modifiers`).
const (
	ModCtrl       byte = 0x01
	ModShift      byte = 0x02
	ModAlt        byte = 0x04
	ModMeta       byte = 0x08
	ModRightCtrl  byte = 0x10
	ModRightShift byte = 0x20
	ModAltGr      byte = 0x40 // AltRight
	ModRightMeta  byte = 0x80
)

// stroke is one key press: usage code plus modifiers.
type stroke struct {
	key byte
	mod byte
}

// usage codes for the keys used by the character tables (keyboardMappings.ts `keys`).
const (
	kDigit1 = 0x1e + iota
	kDigit2
	kDigit3
	kDigit4
	kDigit5
	kDigit6
	kDigit7
	kDigit8
	kDigit9
	kDigit0
	kEnter
	kEscape
	kBackspace
	kTab
	kSpace
	kMinus
	kEqual
	kBracketLeft
	kBracketRight
	kBackslash
	kHashTilde
	kSemicolon
	kQuote
	kBackquote
	kComma
	kPeriod
	kSlash
)

const kIntlBackslash = 0x64

// layouts maps the firmware's layout ISO codes to character tables. The
// tables follow ui/src/keyboardLayouts/en_US.ts and en_UK.ts in
// jetkvm/kvm release/0.5.9.
var layouts = map[string]map[rune]stroke{
	LayoutUS: buildLayout(map[rune]stroke{
		'!': {kDigit1, ModShift}, '@': {kDigit2, ModShift}, '#': {kDigit3, ModShift},
		'$': {kDigit4, ModShift}, '%': {kDigit5, ModShift}, '^': {kDigit6, ModShift},
		'&': {kDigit7, ModShift}, '*': {kDigit8, ModShift}, '(': {kDigit9, ModShift},
		')':  {kDigit0, ModShift},
		'\'': {kQuote, 0}, '"': {kQuote, ModShift},
		'\\': {kBackslash, 0}, '|': {kBackslash, ModShift},
		'`': {kBackquote, 0}, '~': {kBackquote, ModShift},
	}),
	LayoutUK: buildLayout(map[rune]stroke{
		'!': {kDigit1, ModShift}, '"': {kDigit2, ModShift}, '£': {kDigit3, ModShift},
		'$': {kDigit4, ModShift}, '€': {kDigit4, ModAltGr}, '%': {kDigit5, ModShift},
		'^': {kDigit6, ModShift}, '&': {kDigit7, ModShift}, '*': {kDigit8, ModShift},
		'(': {kDigit9, ModShift}, ')': {kDigit0, ModShift},
		'\'': {kQuote, 0}, '@': {kQuote, ModShift},
		// The UI sends Backslash (0x31) for the UK #~ key; Windows and
		// Linux map it to the same key as Non-US # (0x32).
		'#': {kBackslash, 0}, '~': {kBackslash, ModShift},
		'`': {kBackquote, 0}, '¬': {kBackquote, ModShift},
		'\\': {kIntlBackslash, 0}, '|': {kIntlBackslash, ModShift},
	}),
}

// buildLayout adds the keys every Latin layout here shares.
func buildLayout(diff map[rune]stroke) map[rune]stroke {
	m := map[rune]stroke{
		' ': {kSpace, 0}, '\n': {kEnter, 0}, '\t': {kTab, 0},
		'-': {kMinus, 0}, '_': {kMinus, ModShift}, '=': {kEqual, 0}, '+': {kEqual, ModShift},
		'[': {kBracketLeft, 0}, '{': {kBracketLeft, ModShift},
		']': {kBracketRight, 0}, '}': {kBracketRight, ModShift},
		';': {kSemicolon, 0}, ':': {kSemicolon, ModShift},
		',': {kComma, 0}, '<': {kComma, ModShift}, '.': {kPeriod, 0}, '>': {kPeriod, ModShift},
		'/': {kSlash, 0}, '?': {kSlash, ModShift},
	}
	for c := 'a'; c <= 'z'; c++ {
		m[c] = stroke{byte(0x04 + c - 'a'), 0}
		m[c-'a'+'A'] = stroke{byte(0x04 + c - 'a'), ModShift}
	}
	m['1'] = stroke{kDigit1, 0}
	for c := '2'; c <= '9'; c++ {
		m[c] = stroke{byte(kDigit2 + c - '2'), 0}
	}
	m['0'] = stroke{kDigit0, 0}
	for r, s := range diff {
		m[r] = s
	}
	return m
}

// normalizeLayout accepts "en-UK", "en_GB", "uk", "gb", "us", ... .
func normalizeLayout(name string) (string, error) {
	switch strings.ToLower(strings.ReplaceAll(name, "_", "-")) {
	case "en-us", "us", "en":
		return LayoutUS, nil
	case "en-uk", "en-gb", "uk", "gb":
		return LayoutUK, nil
	}
	return "", fmt.Errorf("unsupported keyboard layout %q (supported: en-US, en-UK)", name)
}

// namedKeys are layout-independent keys addressable in combos. Names are
// matched case-insensitively.
var namedKeys = map[string]byte{
	"enter": 0x28, "return": 0x28, "esc": 0x29, "escape": 0x29, "backspace": 0x2a,
	"tab": 0x2b, "space": 0x2c, "capslock": 0x39, "printscreen": 0x46, "prtsc": 0x46,
	"scrolllock": 0x47, "pause": 0x48, "insert": 0x49, "ins": 0x49, "home": 0x4a,
	"pageup": 0x4b, "pgup": 0x4b, "delete": 0x4c, "del": 0x4c, "end": 0x4d,
	"pagedown": 0x4e, "pgdn": 0x4e, "right": 0x4f, "left": 0x50, "down": 0x51, "up": 0x52,
	"numlock": 0x53, "menu": 0x65, "contextmenu": 0x65, "application": 0x65,
	"minus": kMinus, "equal": kEqual, "comma": kComma, "period": kPeriod, "slash": kSlash,
	"backslash": kBackslash, "semicolon": kSemicolon, "quote": kQuote, "backquote": kBackquote,
	"bracketleft": kBracketLeft, "bracketright": kBracketRight, "intlbackslash": kIntlBackslash,
	"kp0": 0x62, "kp1": 0x59, "kp2": 0x5a, "kp3": 0x5b, "kp4": 0x5c, "kp5": 0x5d,
	"kp6": 0x5e, "kp7": 0x5f, "kp8": 0x60, "kp9": 0x61, "kpenter": 0x58,
	"kpplus": 0x57, "kpminus": 0x56, "kpmultiply": 0x55, "kpdivide": 0x54, "kpdecimal": 0x63,
}

func init() {
	for i := 1; i <= 12; i++ {
		namedKeys[fmt.Sprintf("f%d", i)] = byte(0x3a + i - 1)
	}
	for i := 13; i <= 24; i++ {
		namedKeys[fmt.Sprintf("f%d", i)] = byte(0x68 + i - 13)
	}
}

var modifierNames = map[string]byte{
	"ctrl": ModCtrl, "control": ModCtrl, "lctrl": ModCtrl, "rctrl": ModRightCtrl,
	"shift": ModShift, "lshift": ModShift, "rshift": ModRightShift,
	"alt": ModAlt, "lalt": ModAlt, "altgr": ModAltGr, "ralt": ModAltGr,
	"win": ModMeta, "super": ModMeta, "meta": ModMeta, "gui": ModMeta, "cmd": ModMeta,
	"lwin": ModMeta, "rwin": ModRightMeta,
}

// parseCombo turns "ctrl+alt+Delete", "win+x", "shift", "f11" into a
// modifier mask and up to 6 key codes. A single character is resolved
// through the layout (so "ctrl+z" presses the Z key; "ctrl+?" adds shift).
func parseCombo(combo, layout string) (byte, []byte, error) {
	var mod byte
	var keys []byte
	parts := strings.Split(combo, "+")
	// "ctrl++" means ctrl and the plus key.
	if strings.HasSuffix(combo, "++") {
		parts = append(parts[:len(parts)-2], "+")
	}
	for _, p := range parts {
		if p == "" {
			return 0, nil, fmt.Errorf("bad combo %q", combo)
		}
		low := strings.ToLower(p)
		if m, ok := modifierNames[low]; ok {
			mod |= m
			continue
		}
		if k, ok := namedKeys[low]; ok {
			keys = append(keys, k)
			continue
		}
		if r := []rune(p); len(r) == 1 {
			// Letters in combos mean the key, not the shifted character.
			c := r[0]
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			s, ok := layouts[layout][c]
			if !ok {
				return 0, nil, fmt.Errorf("key %q not on layout %s", p, layout)
			}
			mod |= s.mod
			keys = append(keys, s.key)
			continue
		}
		return 0, nil, fmt.Errorf("unknown key %q in combo %q", p, combo)
	}
	if len(keys) > 6 {
		return 0, nil, fmt.Errorf("combo %q has more than 6 keys", combo)
	}
	return mod, keys, nil
}
