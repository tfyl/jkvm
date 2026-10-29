//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// build compiles jkvm once per test run.
var build = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "jkvm-e2e-bin")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "jkvm")
	out, err := exec.Command("go", "build", "-o", bin, "../cmd/jkvm").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("go build: %w\n%s", err, out)
	}
	return bin, nil
})

// host returns JKVM_E2E_HOST, skipping the test when it is unset.
func host(t *testing.T) string {
	t.Helper()
	h := os.Getenv("JKVM_E2E_HOST")
	if h == "" {
		t.Skip("JKVM_E2E_HOST not set")
	}
	return h
}

type result struct {
	code           int
	stdout, stderr string
}

// jkvm runs the binary with args. The device comes from JKVM_E2E_HOST
// unless env overrides JKVM_HOST; JKVM_E2E_PASSWORD is the password.
func jkvm(t *testing.T, env []string, args ...string) result {
	t.Helper()
	bin, err := build()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(),
		"JKVM_HOST="+os.Getenv("JKVM_E2E_HOST"),
		"JKVM_PASSWORD="+os.Getenv("JKVM_E2E_PASSWORD"),
		"JKVM_LAYOUT=")
	cmd.Env = append(cmd.Env, env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	r := result{stdout: stdout.String(), stderr: stderr.String()}
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		r.code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	t.Logf("jkvm %s: exit %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), r.code, trim(r.stdout), trim(r.stderr))
	return r
}

func trim(s string) string {
	if len(s) > 2000 {
		return s[:2000] + "..."
	}
	return s
}

// ok runs jkvm and fails the test unless it exits 0.
func ok(t *testing.T, args ...string) string {
	t.Helper()
	r := jkvm(t, nil, args...)
	if r.code != 0 {
		t.Fatalf("jkvm %q: exit %d: %s", args, r.code, r.stderr)
	}
	return r.stdout
}

func decode[T any](t *testing.T, s string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("bad JSON %q: %v", s, err)
	}
	return v
}

func TestVersion(t *testing.T) {
	host(t)
	for _, args := range [][]string{{"version"}, {"--version"}} {
		if out := ok(t, args...); !strings.HasPrefix(out, "jkvm ") {
			t.Errorf("%q printed %q", args, out)
		}
	}
}

func TestInfo(t *testing.T) {
	host(t)
	info := decode[struct {
		DeviceID     string `json:"getDeviceID"`
		LocalVersion struct {
			App    string `json:"appVersion"`
			System string `json:"systemVersion"`
		} `json:"getLocalVersion"`
		VideoState json.RawMessage `json:"getVideoState"`
		USBState   json.RawMessage `json:"getUSBState"`
	}](t, ok(t, "info"))
	if info.DeviceID == "" || info.LocalVersion.App == "" || info.LocalVersion.System == "" {
		t.Errorf("missing firmware fields: %+v", info)
	}
	if len(info.VideoState) == 0 || len(info.USBState) == 0 {
		t.Errorf("missing video or USB state: %+v", info)
	}
}

func TestRPC(t *testing.T) {
	host(t)
	if got := decode[string](t, ok(t, "rpc", "ping")); got != "pong" {
		t.Errorf("ping = %q, want pong", got)
	}
	atx := decode[map[string]any](t, ok(t, "rpc", "getATXState"))
	if _, has := atx["power"]; !has {
		t.Errorf("getATXState = %v, want a power field", atx)
	}
}

func TestMediaStatus(t *testing.T) {
	host(t)
	st := decode[map[string]json.RawMessage](t, ok(t, "media", "status"))
	for _, k := range []string{"getVirtualMediaState", "getStorageSpace", "listStorageFiles"} {
		if _, has := st[k]; !has {
			t.Errorf("media status lacks %s: %v", k, st)
		}
	}
}

func TestEvents(t *testing.T) {
	host(t)
	out := ok(t, "events", "-t", "3s")
	sc := bufio.NewScanner(strings.NewReader(out))
	n := 0
	for sc.Scan() {
		ev := decode[struct {
			Method string `json:"method"`
			Time   string `json:"time"`
		}](t, sc.Text())
		if ev.Method == "" || ev.Time == "" {
			t.Errorf("bad event line %q", sc.Text())
		}
		n++
	}
	if n == 0 {
		t.Error("no events in 3s")
	}
}

func TestDisplay(t *testing.T) {
	host(t)
	type preset struct {
		Name          string
		Width, Height int
		EDID          string
	}
	ps := decode[[]preset](t, ok(t, "display", "presets"))
	if len(ps) == 0 || ps[0].Name == "" || ps[0].EDID == "" {
		t.Fatalf("presets %+v", ps)
	}
	for _, args := range [][]string{{"display"}, {"display", "status"}} {
		st := decode[struct {
			Ready         bool
			Width, Height int
			Preset, EDID  string
		}](t, ok(t, args...))
		if st.EDID == "" || (st.Ready && st.Width == 0) {
			t.Errorf("%q: %+v", args, st)
		}
	}
}

// videoReady skips the test when the device has no HDMI signal.
func videoReady(t *testing.T) (width, height int) {
	t.Helper()
	st := decode[struct {
		Ready  bool `json:"ready"`
		Width  int  `json:"width"`
		Height int  `json:"height"`
	}](t, ok(t, "rpc", "getVideoState"))
	if !st.Ready || st.Width == 0 {
		t.Skip("no video signal on the device")
	}
	return st.Width, st.Height
}

func TestSnapshot(t *testing.T) {
	host(t)
	w, h := videoReady(t)
	path := filepath.Join(t.TempDir(), "s.png")
	out := ok(t, "snapshot", "-o", path)
	if want := fmt.Sprintf("%s %dx%d\n", path, w, h); out != want {
		t.Errorf("printed %q, want %q", out, want)
	}
	checkPNG(t, path, w, h)
}

func checkPNG(t *testing.T, path string, w, h int) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, err := png.DecodeConfig(f)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if cfg.Width != w || cfg.Height != h {
		t.Errorf("%s is %dx%d, want %dx%d", path, cfg.Width, cfg.Height, w, h)
	}
}

func TestWaitScreenTimeout(t *testing.T) {
	host(t)
	videoReady(t)
	path := filepath.Join(t.TempDir(), "w.png")
	// Half the screen will not change in 3s on an idle target.
	r := jkvm(t, nil, "wait-screen", "-o", path, "-t", "3s", "--threshold", "0.5")
	if r.code != 1 || !strings.Contains(r.stderr, "screen did not change") {
		t.Errorf("exit %d, stderr %q; want 1 and a timeout message", r.code, r.stderr)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("wrote a PNG on timeout")
	}
}

func TestErrors(t *testing.T) {
	host(t)
	for _, c := range []struct {
		name   string
		env    []string
		args   []string
		code   int
		stderr string
	}{
		{"bad host", nil, []string{"--host", "http://127.0.0.1:1", "--timeout", "10s", "rpc", "ping"}, 1, "jkvm: connect to http://127.0.0.1:1"},
		{"no host", []string{"JKVM_HOST="}, []string{"info"}, 2, "no device given"},
		{"unknown command", nil, []string{"frobnicate"}, 2, `unknown command "frobnicate"`},
		{"unknown flag", nil, []string{"info", "--frobnicate"}, 2, "unknown flag"},
		// Connects, but £ is not on en-US, so nothing is typed.
		{"bad layout char", nil, []string{"type", "--layout", "en-US", "a£b"}, 2, "nothing was sent"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := jkvm(t, c.env, c.args...)
			if r.code != c.code || !strings.Contains(r.stderr, c.stderr) {
				t.Errorf("exit %d, stderr %q; want %d and %q", r.code, r.stderr, c.code, c.stderr)
			}
		})
	}
}

// The input tier sends keyboard, mouse and media input to the target. It
// asserts only that each command succeeds, and saves a snapshot after each
// step for a person to check: JKVM_E2E_OUT, else a new temp directory, which
// the test logs.

func TestInput(t *testing.T) {
	host(t)
	if os.Getenv("JKVM_E2E_INPUT") != "1" {
		t.Skip("set JKVM_E2E_INPUT=1 to send input to the target")
	}
	out := os.Getenv("JKVM_E2E_OUT")
	if out == "" {
		var err error
		if out, err = os.MkdirTemp("", "jkvm-e2e-"); err != nil {
			t.Fatal(err)
		}
	} else if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Logf("snapshots in %s", out)
	n := 0
	snap := func(t *testing.T, name string) {
		t.Helper()
		n++
		path := filepath.Join(out, fmt.Sprintf("%02d-%s.png", n, name))
		if r := jkvm(t, nil, "snapshot", "-o", path); r.code != 0 {
			t.Logf("snapshot %s failed: %s", name, r.stderr)
		}
	}
	steps := []struct {
		name string
		args [][]string
	}{
		{"mouse-move-centre", [][]string{{"mouse", "move", "--norm", "0.5", "0.5"}}},
		{"mouse-move-pixels", [][]string{{"mouse", "move", "100", "100"}}},
		{"mouse-click-centre", [][]string{{"mouse", "click", "--norm", "0.5", "0.5"}}},
		{"mouse-scroll-down-up", [][]string{{"mouse", "scroll", "-3"}, {"mouse", "scroll", "3"}}},
		{"key-shift", [][]string{{"key", "shift"}}},
		{"type", [][]string{{"type", "--layout", "en-US", "jkvm e2e"}}},
		{"type-backspace", [][]string{{"key", "backspace", "backspace", "backspace", "backspace", "backspace", "backspace", "backspace", "backspace"}}},
	}
	snap(t, "before")
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			for _, args := range s.args {
				ok(t, args...)
			}
			time.Sleep(300 * time.Millisecond)
			snap(t, s.name)
		})
	}
	t.Run("media", func(t *testing.T) { testMedia(t, snap) })
	t.Run("display-set", func(t *testing.T) { testDisplaySet(t, snap) })
}

// testDisplaySet switches the EDID to a 1920x1200 preset and back to the
// saved one. The target changes resolution twice.
func testDisplaySet(t *testing.T, snap func(*testing.T, string)) {
	type video struct {
		Ready         bool
		Width, Height int
		Preset        string
	}
	before := decode[video](t, ok(t, "display", "status"))
	save := filepath.Join(t.TempDir(), "prev.edid")
	t.Cleanup(func() {
		if b, err := os.ReadFile(save); err == nil {
			jkvm(t, nil, "--timeout", "30s", "display", "set", strings.TrimSpace(string(b)))
		}
	})
	set := decode[video](t, ok(t, "--timeout", "30s", "display", "set", "--save", save, "Acer B246WL, 1920x1200"))
	t.Logf("after set: %+v", set)
	snap(t, "display-acer")
	b, err := os.ReadFile(save)
	if err != nil {
		t.Fatal(err)
	}
	back := decode[video](t, ok(t, "--timeout", "30s", "display", "set", strings.TrimSpace(string(b))))
	if back.Width != before.Width || back.Height != before.Height || back.Preset != before.Preset {
		t.Errorf("restored %+v, before %+v", back, before)
	}
	snap(t, "display-restored")
}

// testMedia uploads a small generated disk image, mounts it, and unmounts
// and deletes it again.
func testMedia(t *testing.T, snap func(*testing.T, string)) {
	name := fmt.Sprintf("jkvm-e2e-%d.img", time.Now().Unix())
	img := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(img, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	type state struct {
		Mounted json.RawMessage `json:"getVirtualMediaState"`
	}
	if st := decode[state](t, ok(t, "media", "status")); string(st.Mounted) != "null" {
		t.Skipf("media already mounted: %s", st.Mounted)
	}
	t.Cleanup(func() {
		jkvm(t, nil, "media", "unmount")
		jkvm(t, nil, "rpc", "deleteStorageFile", fmt.Sprintf(`{"filename":%q}`, name))
	})
	ok(t, "media", "mount-file", "--mode", "Disk", img)
	if st := decode[state](t, ok(t, "media", "status")); !strings.Contains(string(st.Mounted), name) {
		t.Errorf("after mount, media state %s does not name %s", st.Mounted, name)
	}
	time.Sleep(2 * time.Second)
	snap(t, "media-mounted")
	ok(t, "media", "unmount")
	if st := decode[state](t, ok(t, "media", "status")); string(st.Mounted) != "null" {
		t.Errorf("after unmount, media state %s", st.Mounted)
	}
	snap(t, "media-unmounted")
}
