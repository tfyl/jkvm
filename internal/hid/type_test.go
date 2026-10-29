package hid

import (
	"context"
	"strings"
	"testing"

	"github.com/tfyl/jkvm/internal/kvm/kvmtest"
)

// ackMacros makes the fake acknowledge each keyboard macro the way the
// device does: macro state running, then finished.
func ackMacros(d *kvmtest.Device, msg []byte) {
	if len(msg) > 0 && msg[0] == msgKeyboardMacro {
		d.Reply([]byte{msgKeyboardMacroState, 1})
		d.Reply([]byte{msgKeyboardMacroState, 0})
	}
}

func TestTypeSendsMacroInChunks(t *testing.T) {
	d := kvmtest.New(map[string]string{"getKeyboardLayout": `"en-US"`})
	d.OnHID = ackMacros
	text := strings.Repeat("a", macroChunk/2+1) // two steps per character: two chunks
	if err := Type(context.Background(), d, text, TypeOptions{}); err != nil {
		t.Fatal(err)
	}
	sent := d.Sent()
	if len(sent) != 2 {
		t.Fatalf("sent %d macros, want 2", len(sent))
	}
	if got := countSteps(sent[0]) + countSteps(sent[1]); got != 2*len(text) {
		t.Errorf("%d steps, want %d", got, 2*len(text))
	}
}

func countSteps(m []byte) int { return int(m[2])<<24 | int(m[3])<<16 | int(m[4])<<8 | int(m[5]) }

func TestTypeRejectsBeforeSending(t *testing.T) {
	d := kvmtest.New(nil)
	err := Type(context.Background(), d, "a£b", TypeOptions{Layout: "us"})
	if err == nil || !strings.Contains(err.Error(), "nothing was sent") {
		t.Fatalf("err = %v", err)
	}
	if len(d.Sent()) != 0 {
		t.Error("sent input despite an untypable character")
	}
}

func TestTypeCRLF(t *testing.T) {
	d := kvmtest.New(nil)
	d.OnHID = ackMacros
	if err := Type(context.Background(), d, "a\r\nb", TypeOptions{Layout: "us"}); err != nil {
		t.Fatal(err)
	}
	if got := countSteps(d.Sent()[0]); got != 6 {
		t.Errorf("%d steps, want 6 (CRLF is one Enter)", got)
	}
}

func TestKeysNamedOnlySkipsLayoutLookup(t *testing.T) {
	d := kvmtest.New(nil) // no getKeyboardLayout: asking would fail
	d.OnHID = ackMacros
	if err := Keys(context.Background(), d, []string{"ctrl+alt+Delete", "enter"}, "", 0); err != nil {
		t.Fatal(err)
	}
	if len(d.Calls()) != 0 {
		t.Errorf("unexpected RPCs %v", d.Calls())
	}
	// ctrl+alt+Delete: modifiers alone, then with the key, then release;
	// enter: press, release.
	if got := countSteps(d.Sent()[0]); got != 5 {
		t.Errorf("%d steps, want 5", got)
	}
}

func TestMouseScroll(t *testing.T) {
	d := kvmtest.New(map[string]string{"wheelReport": "null"})
	if err := MouseScroll(context.Background(), d, -3, 0); err != nil {
		t.Fatal(err)
	}
	calls := d.Calls()
	if len(calls) != 3 {
		t.Fatalf("%d wheelReport calls, want 3", len(calls))
	}
	for _, c := range calls {
		if c.Params["wheelY"] != -1 {
			t.Errorf("wheelY = %v, want -1", c.Params["wheelY"])
		}
	}
}

func TestNormalizeLayout(t *testing.T) {
	for in, want := range map[string]string{
		"en-US": LayoutUS, "us": LayoutUS, "EN_us": LayoutUS,
		"en-UK": LayoutUK, "en-GB": LayoutUK, "en_GB": LayoutUK, "uk": LayoutUK, "gb": LayoutUK,
	} {
		if got, err := normalizeLayout(in); err != nil || got != want {
			t.Errorf("normalizeLayout(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := normalizeLayout("de-DE"); err == nil {
		t.Error("de-DE: want error")
	}
}

func TestLayoutsCoverPrintableASCII(t *testing.T) {
	for name, table := range layouts {
		for r := rune(' '); r <= '~'; r++ {
			if _, ok := table[r]; !ok {
				t.Errorf("%s cannot type %q", name, r)
			}
		}
		// No two characters may share a key and modifier.
		seen := map[stroke]rune{}
		for r, s := range table {
			if o, dup := seen[s]; dup {
				t.Errorf("%s: %q and %q are both %+v", name, r, o, s)
			}
			seen[s] = r
		}
	}
}
