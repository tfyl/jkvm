// Package display reads and sets the EDID the JetKVM presents to the target
// over HDMI, which decides the video modes the target offers.
package display

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tfyl/jkvm/internal/kvm"
)

// DefaultPreset is the name of the firmware's own EDID preset.
const DefaultPreset = "JetKVM Default"

// Preset is one of the device's built-in EDIDs (getEDIDPresets).
type Preset struct {
	Name string `json:"name"`
	// Width and Height are the preferred mode from the EDID's first detailed
	// timing; zero when it has none.
	Width  int    `json:"width"`
	Height int    `json:"height"`
	EDID   string `json:"edid"`
}

// Video is the captured HDMI input (getVideoState).
type Video struct {
	Ready  bool    `json:"ready"`
	Width  int     `json:"width"`
	Height int     `json:"height"`
	FPS    float64 `json:"fps"`
}

// Status is the video state and the EDID in use.
type Status struct {
	Video
	// Preset names the preset whose EDID is in use, "" for a custom one.
	Preset string `json:"preset"`
	EDID   string `json:"edid"`
}

// Presets returns the device's EDID presets.
func Presets(ctx context.Context, d kvm.RPC) ([]Preset, error) {
	raw, err := d.Call(ctx, "getEDIDPresets", nil)
	if err != nil {
		return nil, fmt.Errorf("getEDIDPresets: %w", err)
	}
	var ps []Preset
	if err := json.Unmarshal(raw, &ps); err != nil {
		return nil, fmt.Errorf("getEDIDPresets: %w", err)
	}
	for i := range ps {
		ps[i].Width, ps[i].Height = PreferredMode(ps[i].EDID)
		if ps[i].Width == 0 {
			ps[i].Width, ps[i].Height = nameMode(ps[i].Name)
		}
	}
	return ps, nil
}

// VideoState returns getVideoState.
func VideoState(ctx context.Context, d kvm.RPC) (Video, error) {
	var v Video
	raw, err := d.Call(ctx, "getVideoState", nil)
	if err != nil {
		return v, fmt.Errorf("getVideoState: %w", err)
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, fmt.Errorf("getVideoState: %w", err)
	}
	return v, nil
}

// EDID returns the EDID in use, as hex.
func EDID(ctx context.Context, d kvm.RPC) (string, error) {
	raw, err := d.Call(ctx, "getEDID", nil)
	if err != nil {
		return "", fmt.Errorf("getEDID: %w", err)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("getEDID: %w", err)
	}
	return s, nil
}

// GetStatus returns the video state, the EDID in use and its preset name.
func GetStatus(ctx context.Context, d kvm.RPC) (Status, error) {
	var st Status
	var err error
	if st.Video, err = VideoState(ctx, d); err != nil {
		return st, err
	}
	if st.EDID, err = EDID(ctx, d); err != nil {
		return st, err
	}
	ps, err := Presets(ctx, d)
	if err != nil {
		return st, err
	}
	st.Preset = match(ps, st.EDID)
	return st, nil
}

func match(ps []Preset, edid string) string {
	for _, p := range ps {
		if strings.EqualFold(p.EDID, edid) {
			return p.Name
		}
	}
	return ""
}

// PreferredMode reads the active size from an EDID's first detailed timing
// descriptor (bytes 54..71). It returns zeros when there is none.
func PreferredMode(edid string) (width, height int) {
	b, err := hex.DecodeString(edid)
	if err != nil || len(b) < 72 || b[54] == 0 && b[55] == 0 {
		return 0, 0
	}
	return int(b[56]) | int(b[58]>>4)<<8, int(b[59]) | int(b[61]>>4)<<8
}

// nameModeRE matches the "1280x1024" that ends a preset name.
var nameModeRE = regexp.MustCompile(`(\d+)x(\d+)$`)

// nameMode reads the size from a preset name such as "DELL IDRAC EDID,
// 1280x1024", for EDIDs with no detailed timing.
func nameMode(name string) (width, height int) {
	m := nameModeRE.FindStringSubmatch(name)
	if m == nil {
		return 0, 0
	}
	width, _ = strconv.Atoi(m[1])
	height, _ = strconv.Atoi(m[2])
	return width, height
}

// ErrBadEDID means the argument is neither a preset name nor an EDID.
var ErrBadEDID = errors.New("not a preset name or EDID hex")

// Resolve turns a preset name (case-insensitive) or EDID hex into hex.
func Resolve(ps []Preset, arg string) (string, error) {
	for _, p := range ps {
		if strings.EqualFold(p.Name, arg) {
			return p.EDID, nil
		}
	}
	s := strings.TrimSpace(arg)
	if b, err := hex.DecodeString(s); err == nil && len(b) >= 128 && len(b)%128 == 0 {
		return s, nil
	}
	names := make([]string, len(ps))
	for i, p := range ps {
		names[i] = fmt.Sprintf("%q", p.Name)
	}
	return "", fmt.Errorf("%w: %q (presets: %s; EDID hex must be a multiple of 128 bytes)", ErrBadEDID, arg, strings.Join(names, ", "))
}

// SetOptions control Set.
type SetOptions struct {
	// Settle is how long to wait after setEDID before polling, so the
	// target has time to drop the old mode. Default 1s.
	Settle time.Duration
	// Interval is the poll interval for getVideoState. Default 500ms.
	Interval time.Duration
}

// Set sets the EDID and waits until the device reports video ready again,
// then returns the new video state. The caller's context bounds the wait.
func Set(ctx context.Context, d kvm.RPC, edid string, opt SetOptions) (Video, error) {
	if opt.Settle <= 0 {
		opt.Settle = time.Second
	}
	if opt.Interval <= 0 {
		opt.Interval = 500 * time.Millisecond
	}
	if _, err := d.Call(ctx, "setEDID", map[string]any{"edid": edid}); err != nil {
		return Video{}, fmt.Errorf("setEDID: %w", err)
	}
	wait := opt.Settle
	for {
		select {
		case <-ctx.Done():
			return Video{}, fmt.Errorf("EDID set, but the video did not come back: %w", ctx.Err())
		case <-time.After(wait):
		}
		wait = opt.Interval
		v, err := VideoState(ctx, d)
		if err != nil {
			if ctx.Err() != nil {
				continue
			}
			return v, err
		}
		if v.Ready && v.Width > 0 {
			return v, nil
		}
	}
}
