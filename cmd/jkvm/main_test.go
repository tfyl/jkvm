package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tfyl/jkvm/internal/kvm/kvmtest"
)

func TestExpandEscapes(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`ls\n`, "ls\n"},
		{`a\tb`, "a\tb"},
		{`C:\\new`, `C:\new`},
		{`C:\Users\x`, `C:\Users\x`},
		{`trailing\`, `trailing\`},
	} {
		if got := expandEscapes(c.in); got != c.want {
			t.Errorf("expandEscapes(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseNumbers(t *testing.T) {
	got, err := parseNumbers([]string{"1", "-2.5"})
	if err != nil || !slices.Equal(got, []float64{1, -2.5}) {
		t.Errorf("got %v, %v", got, err)
	}
	if _, err := parseNumbers([]string{"x"}); err == nil {
		t.Error("want error")
	}
}

func TestNormalizeHost(t *testing.T) {
	for in, want := range map[string]string{
		"jetkvm.local":          "http://jetkvm.local",
		"http://10.0.0.5/":      "http://10.0.0.5",
		"https://kvm.example//": "https://kvm.example",
	} {
		if got := normalizeHost(in); got != want {
			t.Errorf("normalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}

// fake is a kvmtest.Device that the commands can close.
type fake struct{ *kvmtest.Device }

func (fake) Close() error { return nil }

// result is one run of the command line against a fake device.
type result struct {
	code           int
	stdout, stderr string
	dev            *kvmtest.Device
	dialed         bool
	host           string
}

// runFake runs jkvm with args against a fake device that answers the usual
// RPCs and acknowledges keyboard macros.
func runFake(t *testing.T, args ...string) result {
	t.Helper()
	t.Setenv("JKVM_HOST", "")
	t.Setenv("JKVM_LAYOUT", "")
	d := kvmtest.New(map[string]string{
		"ping":              `"pong"`,
		"getKeyboardLayout": `"en-US"`,
		"getVideoState":     `{"ready":true,"width":1920,"height":1080}`,
		"wheelReport":       `null`,
		"getEDID":           `"` + testEDID + `"`,
		"getEDIDPresets":    `[{"name":"JetKVM Default","edid":"` + testEDID + `"},{"name":"Other","edid":"` + otherEDID + `"}]`,
		"setEDID":           `null`,
	})
	d.OnHID = func(d *kvmtest.Device, msg []byte) {
		if len(msg) > 0 && msg[0] == 0x07 { // keyboard macro: report running, then done
			d.Reply([]byte{0x34, 1})
			d.Reply([]byte{0x34, 0})
		}
	}
	var stdout, stderr bytes.Buffer
	r := result{dev: d}
	a := &app{stdout: &stdout, stderr: &stderr}
	a.dial = func(_ context.Context, a *app, _ bool) (device, error) {
		r.dialed, r.host = true, normalizeHost(a.host)
		return fake{d}, nil
	}
	r.code = a.execute(args)
	r.stdout, r.stderr = stdout.String(), stderr.String()
	return r
}

func TestExitCodes(t *testing.T) {
	for _, c := range []struct {
		args []string
		want int
	}{
		{nil, 2},
		{[]string{"info"}, 2}, // no host
		{[]string{"--bogus"}, 2},
		{[]string{"-bogus"}, 2},
		{[]string{"version"}, 0},
		{[]string{"--version"}, 0},
		{[]string{"help"}, 0},
		{[]string{"--help"}, 0},
		{[]string{"completion", "bash"}, 0},
		{[]string{"--host", "jetkvm.test", "nope"}, 2},
		{[]string{"--host", "jetkvm.test", "mouse"}, 2},
		{[]string{"--host", "jetkvm.test", "mouse", "jump"}, 2},
		{[]string{"--host", "jetkvm.test", "mouse", "move", "1"}, 2},
		{[]string{"--host", "jetkvm.test", "mouse", "click", "--button", "up", "1", "2"}, 2},
		{[]string{"--host", "jetkvm.test", "mouse", "scroll", "x"}, 2},
		{[]string{"--host", "jetkvm.test", "media", "mount-url", "--mode", "Floppy", "u"}, 2},
		{[]string{"--host", "jetkvm.test", "media", "eject"}, 2},
		{[]string{"--host", "jetkvm.test", "rpc", "ping", "[1]"}, 2},
		{[]string{"--host", "jetkvm.test", "snapshot"}, 2},
		{[]string{"--host", "jetkvm.test", "type"}, 2},
		{[]string{"--host", "jetkvm.test", "type", "--layout", "us", "£"}, 2},
		{[]string{"--host", "jetkvm.test", "key"}, 2},
		{[]string{"--host", "jetkvm.test", "info", "extra"}, 2},
		{[]string{"--host", "jetkvm.test", "rpc", "ping"}, 0},
		{[]string{"--host", "jetkvm.test", "rpc", "noSuchMethod"}, 1},
	} {
		if r := runFake(t, c.args...); r.code != c.want {
			t.Errorf("jkvm %q = %d, want %d\nstderr: %s", c.args, r.code, c.want, r.stderr)
		}
	}
}

func TestUntypableSendsNothing(t *testing.T) {
	r := runFake(t, "--host", "jetkvm.test", "type", "--layout", "us", "a£b")
	if r.code != 2 || len(r.dev.Sent()) != 0 {
		t.Errorf("code %d, sent %d messages; want 2 and none", r.code, len(r.dev.Sent()))
	}
	if !strings.Contains(r.stderr, "nothing was sent") {
		t.Errorf("stderr %q", r.stderr)
	}
}

func TestFlagPlacement(t *testing.T) {
	for _, args := range [][]string{
		{"--host", "jetkvm.test", "rpc", "ping"},
		{"--host=jetkvm.test", "rpc", "ping"},
		{"rpc", "--host", "jetkvm.test", "ping"},
		{"rpc", "ping", "--host", "jetkvm.test"},
		{"-v", "rpc", "ping", "--timeout", "5s", "--host", "jetkvm.test"},
	} {
		r := runFake(t, args...)
		if r.code != 0 || r.host != "http://jetkvm.test" || !strings.Contains(r.stdout, `"pong"`) {
			t.Errorf("jkvm %q: code %d host %q stdout %q stderr %q", args, r.code, r.host, r.stdout, r.stderr)
		}
	}
}

// wheel returns the wheelY of each wheelReport call.
func wheel(d *kvmtest.Device) []any {
	var ys []any
	for _, c := range d.Calls() {
		if c.Method == "wheelReport" {
			ys = append(ys, c.Params["wheelY"])
		}
	}
	return ys
}

func TestScrollNegative(t *testing.T) {
	for _, args := range [][]string{
		{"--host", "jetkvm.test", "mouse", "scroll", "-3"},
		{"mouse", "scroll", "-3", "--host", "jetkvm.test"},
		{"mouse", "--host", "jetkvm.test", "scroll", "-3"},
		{"mouse", "scroll", "--host=jetkvm.test", "-3", "-v"},
	} {
		r := runFake(t, args...)
		if got := wheel(r.dev); r.code != 0 || !slices.Equal(got, []any{-1, -1, -1}) {
			t.Errorf("jkvm %q: code %d, wheelY %v, stderr %q", args, r.code, got, r.stderr)
		}
	}
	r := runFake(t, "--host", "jetkvm.test", "mouse", "scroll", "2")
	if got := wheel(r.dev); r.code != 0 || !slices.Equal(got, []any{1, 1}) {
		t.Errorf("scroll 2: code %d, wheelY %v", r.code, got)
	}
}

func TestMouseNegativeKeepsOrder(t *testing.T) {
	// hid rejects -0.5 and names the point, which shows the argument order:
	// the old flag-package workaround turned "-0.5 0.25" into "0.25 -0.5".
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"0.25", "-0.5"}, "(0.25,-0.5)"},
		{[]string{"-0.5", "0.25"}, "(-0.5,0.25)"},
	} {
		r := runFake(t, append([]string{"--host", "jetkvm.test", "mouse", "move", "-n"}, c.args...)...)
		if r.code != 1 || !strings.Contains(r.stderr, c.want) {
			t.Errorf("move %q: code %d stderr %q, want %s", c.args, r.code, r.stderr, c.want)
		}
	}
}

// macroSteps counts the steps in the keyboard macros sent: two per character.
func macroSteps(d *kvmtest.Device) int {
	n := 0
	for _, m := range d.Sent() {
		if len(m) >= 6 && m[0] == 0x07 {
			n += int(m[2])<<24 | int(m[3])<<16 | int(m[4])<<8 | int(m[5])
		}
	}
	return n
}

func TestTypeArgs(t *testing.T) {
	for _, c := range []struct {
		args  []string
		chars int
	}{
		{[]string{"type", "--", "-text"}, 5},
		{[]string{"type", "-e", `a\nb`}, 3},
		{[]string{"type", `a\nb`}, 4},
		{[]string{"type", `a\nb`, "-e"}, 3},
		{[]string{"type", "-l", "en-UK", "-d", "5", "hi"}, 2},
	} {
		r := runFake(t, append([]string{"--host", "jetkvm.test"}, c.args...)...)
		if r.code != 0 || macroSteps(r.dev) != 2*c.chars {
			t.Errorf("jkvm %q: code %d, %d steps, want %d; stderr %q", c.args, r.code, macroSteps(r.dev), 2*c.chars, r.stderr)
		}
	}
}

func TestLayoutEnv(t *testing.T) {
	// JKVM_LAYOUT is read when the command line is built, so set it inside.
	t.Setenv("JKVM_LAYOUT", "en-UK")
	var stdout, stderr bytes.Buffer
	d := kvmtest.New(nil) // no getKeyboardLayout: asking the device would fail
	d.OnHID = func(d *kvmtest.Device, msg []byte) {
		if msg[0] == 0x07 {
			d.Reply([]byte{0x34, 1})
			d.Reply([]byte{0x34, 0})
		}
	}
	a := &app{stdout: &stdout, stderr: &stderr, dial: func(context.Context, *app, bool) (device, error) { return fake{d}, nil }}
	if code := a.execute([]string{"--host", "x", "type", "£"}); code != 0 {
		t.Errorf("code %d: %s", code, stderr.String())
	}
}

func TestHelpOutput(t *testing.T) {
	r := runFake(t, "--help")
	for _, want := range []string{"--host", "--password", "--timeout", "-v, --verbose", "snapshot", "wait-screen", "mouse", "completion"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("--help lacks %q", want)
		}
	}
	r = runFake(t, "mouse", "scroll", "--help")
	if r.code != 0 || !strings.Contains(r.stdout, "jkvm mouse scroll -3") || r.dialed {
		t.Errorf("mouse scroll --help: code %d dialed %v stdout %q", r.code, r.dialed, r.stdout)
	}
	r = runFake(t, "wait-screen", "-h")
	if r.code != 0 || !strings.Contains(r.stdout, "-t, --wait duration") || !strings.Contains(r.stdout, "-o, --output") {
		t.Errorf("wait-screen -h: %q", r.stdout)
	}
	r = runFake(t)
	if r.code != 2 || !strings.Contains(r.stderr, "Available Commands") || r.stdout != "" {
		t.Errorf("bare jkvm: code %d, stdout %q", r.code, r.stdout)
	}
	r = runFake(t, "--version")
	if r.code != 0 || !strings.HasPrefix(r.stdout, "jkvm ") {
		t.Errorf("--version: %q", r.stdout)
	}
	r = runFake(t, "--host", "jetkvm.test", "nope")
	if !strings.HasPrefix(r.stderr, "jkvm: unknown command") || strings.Count(r.stderr, "unknown command") != 1 {
		t.Errorf("errors must print once as jkvm: <err>; got %q", r.stderr)
	}
}

// testEDID and otherEDID are 128-byte EDIDs whose first detailed timing is
// 1920x1080 and 1920x1200.
var (
	testEDID  = "00ffffffffffff00" + strings.Repeat("00", 46) + "023a801871382d40582c4500" + strings.Repeat("00", 62)
	otherEDID = "00ffffffffffff00" + strings.Repeat("00", 46) + "283c80a070b0234030203600" + strings.Repeat("00", 62)
)

func TestDisplay(t *testing.T) {
	displaySettle.Settle = time.Millisecond
	t.Cleanup(func() { displaySettle.Settle = 0 })
	for _, args := range [][]string{{"display"}, {"display", "status"}} {
		r := runFake(t, append([]string{"--host", "x"}, args...)...)
		if r.code != 0 || !strings.Contains(r.stdout, `"preset": "JetKVM Default"`) || !strings.Contains(r.stdout, `"width": 1920`) {
			t.Errorf("%q: code %d stdout %s stderr %s", args, r.code, r.stdout, r.stderr)
		}
	}
	r := runFake(t, "--host", "x", "display", "presets")
	if r.code != 0 || !strings.Contains(r.stdout, `"height": 1200`) {
		t.Errorf("presets: %d %s", r.code, r.stdout)
	}

	save := filepath.Join(t.TempDir(), "prev.edid")
	r = runFake(t, "--host", "x", "display", "set", "--save", save, "other")
	if r.code != 0 || !strings.Contains(r.stdout, `"preset": "Other"`) || !strings.Contains(r.stdout, `"previous": "`+testEDID) {
		t.Fatalf("set: code %d stdout %s stderr %s", r.code, r.stdout, r.stderr)
	}
	if b, _ := os.ReadFile(save); string(b) != testEDID+"\n" {
		t.Errorf("saved %q", b)
	}
	var set []any
	for _, c := range r.dev.Calls() {
		if c.Method == "setEDID" {
			set = append(set, c.Params["edid"])
		}
	}
	if !slices.Equal(set, []any{otherEDID}) {
		t.Errorf("setEDID calls %v", set)
	}

	r = runFake(t, "--host", "x", "display", "set", strings.ToUpper(otherEDID))
	if r.code != 0 || !strings.Contains(r.stdout, `"preset": "Other"`) {
		t.Errorf("set hex: %d %s %s", r.code, r.stdout, r.stderr)
	}
	r = runFake(t, "--host", "x", "display", "reset")
	if r.code != 0 || !strings.Contains(r.stdout, `"preset": "JetKVM Default"`) {
		t.Errorf("reset: %d %s %s", r.code, r.stdout, r.stderr)
	}
	for _, args := range [][]string{{"display", "set"}, {"display", "set", "nope"}, {"display", "bogus"}} {
		r := runFake(t, append([]string{"--host", "x"}, args...)...)
		if r.code != 2 || slices.ContainsFunc(r.dev.Calls(), func(c kvmtest.Call) bool { return c.Method == "setEDID" }) {
			t.Errorf("%q: code %d, want 2 and no setEDID; %s", args, r.code, r.stderr)
		}
	}
}
