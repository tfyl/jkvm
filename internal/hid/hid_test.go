package hid

import (
	"bytes"
	"testing"
)

func TestLayouts(t *testing.T) {
	cases := []struct {
		layout string
		r      rune
		want   stroke
	}{
		{LayoutUS, '@', stroke{kDigit2, ModShift}},
		{LayoutUK, '@', stroke{kQuote, ModShift}},
		{LayoutUS, '"', stroke{kQuote, ModShift}},
		{LayoutUK, '"', stroke{kDigit2, ModShift}},
		{LayoutUK, '\\', stroke{kIntlBackslash, 0}},
		{LayoutUK, '£', stroke{kDigit3, ModShift}},
		{LayoutUK, '€', stroke{kDigit4, ModAltGr}},
		{LayoutUS, 'Q', stroke{0x14, ModShift}},
		{LayoutUS, '0', stroke{0x27, 0}},
		{LayoutUS, '9', stroke{0x26, 0}},
		{LayoutUS, '2', stroke{0x1f, 0}},
		{LayoutUS, '\n', stroke{0x28, 0}},
	}
	for _, c := range cases {
		if got := layouts[c.layout][c.r]; got != c.want {
			t.Errorf("%s %q = %+v, want %+v", c.layout, c.r, got, c.want)
		}
	}
	if _, ok := layouts[LayoutUS]['£']; ok {
		t.Error("US layout should not have £")
	}
}

func TestParseCombo(t *testing.T) {
	cases := []struct {
		in   string
		mod  byte
		keys []byte
	}{
		{"win+x", ModMeta, []byte{0x1b}},
		{"ctrl+alt+Delete", ModCtrl | ModAlt, []byte{0x4c}},
		{"enter", 0, []byte{0x28}},
		{"F11", 0, []byte{0x44}},
		{"shift", ModShift, nil},
		{"ctrl+shift+Esc", ModCtrl | ModShift, []byte{0x29}},
		{"ctrl++", ModCtrl | ModShift, []byte{kEqual}},
		{"alt+F4", ModAlt, []byte{0x3d}},
		{"f13", 0, []byte{0x68}},
	}
	for _, c := range cases {
		mod, keys, err := parseCombo(c.in, LayoutUS)
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if mod != c.mod || !bytes.Equal(keys, c.keys) {
			t.Errorf("%s = %#x %v, want %#x %v", c.in, mod, keys, c.mod, c.keys)
		}
	}
	for _, bad := range []string{"", "ctrl+", "foo", "win+nope"} {
		if _, _, err := parseCombo(bad, LayoutUS); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}

func TestEncodeMacro(t *testing.T) {
	got := encodeMacro([]macroStep{{mod: ModShift, keys: []byte{0x04}, delay: 20}, {delay: 300}}, true)
	want := []byte{0x07, 1, 0, 0, 0, 2,
		0x02, 0x04, 0, 0, 0, 0, 0, 0, 20,
		0, 0, 0, 0, 0, 0, 0, 0x01, 0x2c}
	if !bytes.Equal(got, want) {
		t.Errorf("got % x\nwant % x", got, want)
	}
}

func TestToAbs(t *testing.T) {
	x, y, err := toAbs(Point{X: 1919, Y: 0, Width: 1920, Height: 1080})
	if err != nil || x > absMax || x < absMax-20 || y < 1 || y > 20 {
		t.Errorf("got %d %d %v", x, y, err)
	}
	x, y, _ = toAbs(Point{X: 0.5, Y: 1, Normalized: true})
	if x != 16384 || y != absMax {
		t.Errorf("normalised: %d %d", x, y)
	}
	if _, _, err := toAbs(Point{X: 1920, Y: 0, Width: 1920, Height: 1080}); err == nil {
		t.Error("want out-of-range error")
	}
}
