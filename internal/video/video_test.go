package video

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
)

func TestSplitNALs(t *testing.T) {
	b := []byte{0, 0, 0, 1, 0x67, 1, 2, 0, 0, 1, 0x68, 3, 0, 0, 0, 1, 0x65, 4, 5, 0}
	got := splitNALs(b)
	want := [][]byte{{0x67, 1, 2}, {0x68, 3}, {0x65, 4, 5, 0}}
	if len(got) != len(want) {
		t.Fatalf("got %d NALs, want %d: %x", len(got), len(want), got)
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("NAL %d = %x, want %x", i, got[i], want[i])
		}
	}
	au := inspect(b)
	if !au.idr || !bytes.Equal(au.sps, want[0]) || !bytes.Equal(au.pps, want[1]) {
		t.Errorf("inspect = %+v", au)
	}
	if splitNALs([]byte{1, 2, 3}) != nil {
		t.Error("expected no NALs without a start code")
	}
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestSplitPNGs(t *testing.T) {
	a := encodePNG(t, image.NewGray(image.Rect(0, 0, 4, 3)))
	b := encodePNG(t, image.NewRGBA(image.Rect(0, 0, 5, 2)))
	cat := append(append(append([]byte{}, a...), b...), b[:len(b)/2]...)
	got := splitPNGs(cat)
	if len(got) != 2 || !bytes.Equal(got[0], a) || !bytes.Equal(got[1], b) {
		t.Fatalf("splitPNGs returned %d images", len(got))
	}
}

func TestChangedFraction(t *testing.T) {
	mk := func(f func(x, y int) uint8) *thumb {
		img := image.NewRGBA(image.Rect(0, 0, 640, 360))
		for y := range 360 {
			for x := range 640 {
				v := f(x, y)
				img.SetRGBA(x, y, color.RGBA{v, v, v, 255})
			}
		}
		return newThumb(img)
	}
	base := mk(func(x, _ int) uint8 { return uint8(20 + x/3) })
	// Per-pixel noise of +-6 levels: block means stay put.
	noisy := mk(func(x, y int) uint8 { return uint8(20+x/3) + uint8((x*7+y*13)%13) - 6 })
	if f := changedFraction(base, noisy); f != 0 {
		t.Errorf("noise counted as change: %v", f)
	}
	// A white box over a quarter of the screen.
	boxed := mk(func(x, y int) uint8 {
		if x < 320 && y < 180 {
			return 255
		}
		return uint8(20 + x/3)
	})
	if f := changedFraction(base, boxed); f < 0.2 || f > 0.26 {
		t.Errorf("quarter box changed fraction = %v", f)
	}
	small := newThumb(image.NewRGBA(image.Rect(0, 0, 320, 180)))
	if f := changedFraction(base, small); f != 1 {
		t.Errorf("size change fraction = %v, want 1", f)
	}
}

// clip is an encoded test stream split into access units (Annex-B).
type clip struct{ aus [][]byte }

func makeClip(t *testing.T, src string) clip {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	// Baseline, no B-frames, a single IDR: like the device's stream.
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", src, "-frames:v", "3",
		"-c:v", "libx264", "-profile:v", "baseline", "-g", "1000", "-bf", "0",
		"-pix_fmt", "yuv420p", "-f", "h264", "pipe:1").Output()
	if err != nil {
		t.Fatalf("ffmpeg encode: %v", err)
	}
	var c clip
	var cur []byte
	sawSlice := false
	for _, n := range splitNALs(out) {
		typ := nalType(n)
		startsAU := typ == nalSPS || typ == 9 || ((typ == nalSlice || typ == nalIDR) && sawSlice)
		if startsAU && cur != nil {
			c.aus = append(c.aus, cur)
			cur, sawSlice = nil, false
		}
		cur = appendNAL(cur, n)
		if typ == nalSlice || typ == nalIDR {
			sawSlice = true
		}
	}
	c.aus = append(c.aus, cur)
	if len(c.aus) != 3 || !inspect(c.aus[0]).idr {
		t.Fatalf("unexpected clip structure: %d AUs", len(c.aus))
	}
	return c
}

// fakeTrack plays an IDR plus one P frame of the selected clip whenever a
// PLI arrives, like the device does.
type fakeTrack struct {
	mu   sync.Mutex
	pkts chan *rtp.Packet
	seq  uint16
	ts   uint32
	plis int
	pick func(n int) *clip // clip for the n-th PLI (0-based); nil = ignore
}

func newFakeTrack(pick func(int) *clip) *fakeTrack {
	return &fakeTrack{pkts: make(chan *rtp.Packet, 1<<16), pick: pick}
}

func (f *fakeTrack) read() (*rtp.Packet, error) { return <-f.pkts, nil }

func (f *fakeTrack) pli() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.pick(f.plis)
	f.plis++
	if c == nil {
		return nil
	}
	pay := &codecs.H264Payloader{}
	for _, au := range c.aus[:2] {
		f.ts += 3000
		var payloads [][]byte
		for _, n := range splitNALs(au) {
			payloads = append(payloads, pay.Payload(1200, n)...)
		}
		for i, p := range payloads {
			f.seq++
			f.pkts <- &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 102,
				SequenceNumber: f.seq, Timestamp: f.ts, Marker: i == len(payloads)-1}, Payload: p}
		}
	}
	return nil
}

func TestSnapshotDecode(t *testing.T) {
	c := makeClip(t, "testsrc=size=1280x720:rate=30")
	ft := newFakeTrack(func(int) *clip { return &c })
	s := newStream(ft.read, ft.pli)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	f, th, err := s.grab(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if f.Width != 1280 || f.Height != 720 {
		t.Fatalf("size %dx%d", f.Width, f.Height)
	}
	// Compare with ffmpeg's own decode of the same two frames.
	want, err := decodePNG(ctx, append(append([]byte{}, c.aus[0]...), c.aus[1]...))
	if err != nil {
		t.Fatal(err)
	}
	img, _ := png.Decode(bytes.NewReader(want))
	if d := changedFraction(newThumb(img), th); d != 0 {
		t.Errorf("decoded frame differs from reference by %v", d)
	}
}

func TestKeyframeTimeout(t *testing.T) {
	old := keyframeTimeout
	keyframeTimeout = 300 * time.Millisecond
	defer func() { keyframeTimeout = old }()
	ft := newFakeTrack(func(int) *clip { return nil })
	s := newStream(ft.read, ft.pli)
	_, _, err := s.grab(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no keyframe") {
		t.Fatalf("err = %v", err)
	}
}

func TestWaitScreen(t *testing.T) {
	red := makeClip(t, "color=c=red:size=640x360:rate=30")
	blue := makeClip(t, "color=c=blue:size=640x360:rate=30")
	opt := WaitOptions{Interval: 50 * time.Millisecond}

	t.Run("changes", func(t *testing.T) {
		ft := newFakeTrack(func(n int) *clip {
			if n < 3 {
				return &red
			}
			return &blue
		})
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		f, err := newStream(ft.read, ft.pli).waitChange(ctx, opt)
		if err != nil {
			t.Fatal(err)
		}
		img, _ := png.Decode(bytes.NewReader(f.PNG))
		if r, _, b, _ := img.At(10, 10).RGBA(); b < r {
			t.Errorf("returned frame is not the blue one")
		}
	})

	t.Run("timeout", func(t *testing.T) {
		ft := newFakeTrack(func(int) *clip { return &red })
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err := newStream(ft.read, ft.pli).waitChange(ctx, opt)
		if err == nil || !strings.Contains(err.Error(), "did not change") || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v", err)
		}
	})
}
