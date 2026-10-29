// Package video turns the device's H.264 WebRTC track into still images by
// piping depacketised access units into the system ffmpeg.
//
// The device only sends an IDR frame (with SPS/PPS) in response to an RTCP
// PLI, so every grab asks for a keyframe, waits for a complete IDR access
// unit, appends the next frame when one follows quickly, and decodes that
// short stream. The stream stays open across grabs in WaitScreen.
package video

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"strings"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/pion/webrtc/v4/pkg/media/samplebuilder"

	"github.com/tfyl/jkvm/internal/kvm"
)

// ErrNoKeyframe means the device never answered keyframe requests. It has
// been seen once on a live device; a fresh session fixed it.
var ErrNoKeyframe = errors.New("no keyframe from the device")

// Frame is one decoded still.
type Frame struct {
	PNG           []byte
	Width, Height int
}

// Timing knobs; variables so tests can shorten them.
var (
	pliInterval     = 500 * time.Millisecond // re-send PLI until an IDR arrives
	followWait      = 300 * time.Millisecond // how long to wait for the frame after the IDR
	keyframeTimeout = 10 * time.Second       // give up when no IDR arrives at all
)

// Snapshot returns the current screen, decoded from a fresh keyframe.
func Snapshot(ctx context.Context, d kvm.Device) (*Frame, error) {
	s, err := open(ctx, d)
	if err != nil {
		return nil, err
	}
	defer s.close()
	f, _, err := s.grab(ctx)
	return f, err
}

// WaitOptions controls WaitScreen.
type WaitOptions struct {
	Interval  time.Duration // between polls; 0 = 1s
	Threshold float64       // fraction of changed pixels counting as "changed"; 0 = 0.01
}

// WaitScreen polls frames until the screen differs from the first frame by
// more than Threshold, then returns the new frame. ctx bounds the wait.
func WaitScreen(ctx context.Context, d kvm.Device, opt WaitOptions) (*Frame, error) {
	s, err := open(ctx, d)
	if err != nil {
		return nil, err
	}
	defer s.close()
	return s.waitChange(ctx, opt)
}

func (s *stream) waitChange(ctx context.Context, opt WaitOptions) (*Frame, error) {
	if opt.Interval <= 0 {
		opt.Interval = time.Second
	}
	if opt.Threshold <= 0 {
		opt.Threshold = 0.01
	}
	_, base, err := s.grab(ctx)
	if err != nil {
		return nil, fmt.Errorf("baseline frame: %w", err)
	}
	maxSeen := 0.0
	unchanged := func() error {
		return fmt.Errorf("screen did not change by more than %.2f%% (largest change %.2f%%): %w",
			100*opt.Threshold, 100*maxSeen, ctx.Err())
	}
	for {
		select {
		case <-time.After(opt.Interval):
		case <-ctx.Done():
			return nil, unchanged()
		}
		f, t, err := s.grab(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil, unchanged()
			}
			return nil, err
		}
		frac := changedFraction(base, t)
		if frac > opt.Threshold {
			return f, nil
		}
		maxSeen = max(maxSeen, frac)
	}
}

// videoState is the getVideoState result and the videoInputState event payload.
type videoState struct {
	Ready  bool    `json:"ready"`
	Error  string  `json:"error"`
	Width  int     `json:"width"`
	Height int     `json:"height"`
	FPS    float64 `json:"fps"`
}

func (v videoState) err() error {
	switch {
	case v.Error == "no_signal":
		return errors.New("no HDMI signal (the PC is off or its display is asleep)")
	case v.Error != "":
		return fmt.Errorf("video input error: %s", v.Error)
	case !v.Ready:
		return errors.New("video input not ready")
	}
	return nil
}

// stream reads the video track in the background and hands out depacketised
// samples.
type stream struct {
	samples chan *media.Sample
	errc    chan error // terminal read or signal errors
	pli     func() error
	stop    func()

	sps, pps []byte // last parameter sets seen in-band
}

// open checks the HDMI input, attaches to the video track and starts reading.
func open(ctx context.Context, d kvm.Device) (*stream, error) {
	if err := checkFFmpeg(); err != nil {
		return nil, err
	}
	raw, err := d.Call(ctx, "getVideoState", nil)
	if err != nil {
		return nil, fmt.Errorf("getVideoState: %w", err)
	}
	var st videoState
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("getVideoState: %w", err)
	}
	if err := st.err(); err != nil {
		return nil, err
	}
	track, err := d.VideoTrack(ctx)
	if err != nil {
		return nil, err
	}
	if mt := track.Codec().MimeType; !strings.EqualFold(mt, webrtc.MimeTypeH264) {
		return nil, fmt.Errorf("video track is %s, want %s", mt, webrtc.MimeTypeH264)
	}
	_ = track.SetReadDeadline(time.Time{})
	s := newStream(func() (*rtp.Packet, error) {
		p, _, err := track.ReadRTP()
		return p, err
	}, d.RequestKeyframe)
	done := make(chan struct{})
	s.stop = func() {
		close(done)
		_ = track.SetReadDeadline(time.Now())
	}
	go s.watchEvents(d.Events(), done)
	return s, nil
}

func newStream(read func() (*rtp.Packet, error), pli func() error) *stream {
	s := &stream{
		samples: make(chan *media.Sample, 256),
		errc:    make(chan error, 1),
		pli:     pli,
		stop:    func() {},
	}
	go s.readLoop(read)
	return s
}

func (s *stream) close() { s.stop() }

func (s *stream) fail(err error) {
	select {
	case s.errc <- err:
	default:
	}
}

func (s *stream) readLoop(read func() (*rtp.Packet, error)) {
	// maxLate must exceed the packet count of a 1080p IDR; the time bound
	// keeps a lost packet from stalling the builder for long.
	sb := samplebuilder.New(4096, &codecs.H264Packet{}, 90000,
		samplebuilder.WithMaxTimeDelay(time.Second))
	for {
		p, err := read()
		if err != nil {
			s.fail(fmt.Errorf("reading video: %w", err))
			return
		}
		sb.Push(p)
		for smp := sb.Pop(); smp != nil; smp = sb.Pop() {
			select {
			case s.samples <- smp:
			default: // nobody is grabbing; drop
			}
		}
	}
}

func (s *stream) watchEvents(ev <-chan kvm.Event, done <-chan struct{}) {
	for {
		select {
		case e, ok := <-ev:
			if !ok {
				return
			}
			if e.Method != "videoInputState" {
				continue
			}
			var st videoState
			if json.Unmarshal(e.Params, &st) == nil {
				if err := st.err(); err != nil {
					s.fail(err)
				}
			}
		case <-done:
			return
		}
	}
}

// grab decodes one fresh frame.
func (s *stream) grab(ctx context.Context) (*Frame, *thumb, error) {
	annexB, err := s.keyframe(ctx)
	if err != nil {
		return nil, nil, err
	}
	pngData, err := decodePNG(ctx, annexB)
	if err != nil {
		return nil, nil, err
	}
	img, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		return nil, nil, fmt.Errorf("decoding ffmpeg output: %w", err)
	}
	b := img.Bounds()
	return &Frame{PNG: pngData, Width: b.Dx(), Height: b.Dy()}, newThumb(img), nil
}

// keyframe requests an IDR and returns an Annex-B stream starting with
// SPS, PPS and a complete IDR access unit, plus the following frame if it
// arrives promptly.
func (s *stream) keyframe(ctx context.Context) ([]byte, error) {
	// Discard anything buffered before the request.
	for len(s.samples) > 0 {
		<-s.samples
	}
	if err := s.pli(); err != nil {
		return nil, fmt.Errorf("requesting keyframe: %w", err)
	}
	tick := time.NewTicker(pliInterval)
	defer tick.Stop()
	giveUp := time.NewTimer(keyframeTimeout)
	defer giveUp.Stop()

	var (
		buf    []byte
		follow <-chan time.Time
	)
	for {
		select {
		case smp := <-s.samples:
			au := inspect(smp.Data)
			if au.sps != nil {
				s.sps = au.sps
			}
			if au.pps != nil {
				s.pps = au.pps
			}
			if buf != nil {
				if smp.PrevDroppedPackets == 0 {
					buf = append(buf, smp.Data...)
				}
				return buf, nil
			}
			// An IDR right after a gap may have lost its parameter-set
			// packets; the samplebuilder only emits contiguous samples, so
			// the IDR itself is complete.
			if !au.idr || s.sps == nil || s.pps == nil {
				continue
			}
			if au.sps == nil || au.pps == nil {
				buf = appendNAL(appendNAL(nil, s.sps), s.pps)
			}
			buf = append(buf, smp.Data...)
			follow = time.After(followWait)
		case <-follow:
			return buf, nil
		case <-tick.C:
			if buf == nil {
				if err := s.pli(); err != nil {
					return nil, fmt.Errorf("requesting keyframe: %w", err)
				}
			}
		case <-giveUp.C:
			return nil, fmt.Errorf("%w within %s", ErrNoKeyframe, keyframeTimeout)
		case err := <-s.errc:
			s.fail(err) // keep it for later grabs
			return nil, err
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for keyframe: %w", ctx.Err())
		}
	}
}
