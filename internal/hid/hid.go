// Package hid encodes keyboard and mouse input as hidrpc messages (and the
// JSON-RPC fallbacks where hidrpc has no message type, e.g. the wheel).
package hid

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/tfyl/jkvm/internal/kvm"
)

// Layout names accepted by Type and Keys. Values follow the firmware's
// keyboard layout ISO codes ("en-US", "en-UK", ...).
const (
	LayoutUS = "en-US"
	LayoutUK = "en-UK"
)

// hidrpc message types (internal/hidrpc/hidrpc.go).
const (
	msgKeyboardReport     = 0x02
	msgPointerReport      = 0x03
	msgKeyboardMacro      = 0x07
	msgCancelMacro        = 0x08
	msgKeyboardMacroState = 0x34
)

// TypeOptions controls Type.
type TypeOptions struct {
	Layout string // "" = ask the device (getKeyboardLayout), else LayoutUS/LayoutUK
	// Delay between characters; 0 = sensible default.
	DelayMS int
}

// resolveLayout returns the layout to use: the explicit one, else the
// device's configured paste layout.
func resolveLayout(ctx context.Context, d kvm.Device, layout string) (string, error) {
	if layout == "" {
		raw, err := d.Call(ctx, "getKeyboardLayout", nil)
		if err != nil {
			return "", err
		}
		if err := json.Unmarshal(raw, &layout); err != nil {
			return "", fmt.Errorf("getKeyboardLayout: %w", err)
		}
	}
	return normalizeLayout(layout)
}

// macroStep is one step of a keyboard macro: report, then hold for delay.
type macroStep struct {
	mod   byte
	keys  []byte
	delay uint16
}

// UntypableError is returned by Type when the layout cannot produce some of
// the characters. Nothing has been sent to the device.
type UntypableError struct {
	Layout string
	Chars  []string // quoted runes, in the order they appear
}

func (e *UntypableError) Error() string {
	return fmt.Sprintf("layout %s cannot type %s; nothing was sent", e.Layout, strings.Join(e.Chars, " "))
}

// Type types text on the target with correct shift/AltGr handling for the
// layout. Characters the layout cannot produce are an error before anything
// is sent. It uses the device-side keyboard macro (as the UI's paste does),
// so timing does not depend on the network.
func Type(ctx context.Context, d kvm.Device, text string, opt TypeOptions) error {
	layout, err := resolveLayout(ctx, d, opt.Layout)
	if err != nil {
		return err
	}
	delay := opt.DelayMS
	if delay <= 0 {
		delay = 20 // the UI's paste default
	}
	if delay > math.MaxUint16 {
		return fmt.Errorf("delay %dms too large", delay)
	}
	table := layouts[layout]
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var steps []macroStep
	var bad []string
	for _, r := range text {
		s, ok := table[r]
		if !ok {
			bad = append(bad, fmt.Sprintf("%q", r))
			continue
		}
		steps = append(steps,
			macroStep{mod: s.mod, keys: []byte{s.key}, delay: 20},
			macroStep{delay: uint16(delay)})
	}
	if len(bad) > 0 {
		return &UntypableError{Layout: layout, Chars: bad}
	}
	return runMacro(ctx, d, steps)
}

// macroChunk matches the UI: 128 steps per report keeps messages well under
// the 64 KiB data channel limit and never splits a press from its release.
const macroChunk = 128

// runMacro sends steps in chunks and waits for each chunk's macro-state
// "finished" acknowledgement (0x34 [0,_]) before the next. Closing the
// session mid-macro would cancel it on the device, so this must not return
// early on success.
func runMacro(ctx context.Context, d kvm.Device, steps []macroStep) error {
	for i := 0; i < len(steps); i += macroChunk {
		chunk := steps[i:min(i+macroChunk, len(steps))]
		var total time.Duration
		for _, s := range chunk {
			total += time.Duration(s.delay) * time.Millisecond
		}
		drainHID(d)
		if err := d.SendHID(encodeMacro(chunk, true)); err != nil {
			return fmt.Errorf("send keyboard macro: %w", err)
		}
		if err := waitMacro(ctx, d, total+3*time.Second); err != nil {
			_ = d.SendHID([]byte{msgCancelMacro})
			return err
		}
	}
	return nil
}

func encodeMacro(steps []macroStep, isPaste bool) []byte {
	b := make([]byte, 6, 6+9*len(steps))
	b[0] = msgKeyboardMacro
	if isPaste {
		b[1] = 1
	}
	binary.BigEndian.PutUint32(b[2:6], uint32(len(steps)))
	for _, s := range steps {
		var keys [6]byte
		copy(keys[:], s.keys)
		b = append(b, s.mod)
		b = append(b, keys[:]...)
		b = binary.BigEndian.AppendUint16(b, s.delay)
	}
	return b
}

func drainHID(d kvm.Device) {
	for {
		select {
		case <-d.HIDMessages():
		default:
			return
		}
	}
}

func waitMacro(ctx context.Context, d kvm.Device, limit time.Duration) error {
	t := time.NewTimer(limit)
	defer t.Stop()
	started := false
	for {
		select {
		case m := <-d.HIDMessages():
			if len(m) < 2 || m[0] != msgKeyboardMacroState {
				continue
			}
			if m[1] == 1 {
				started = true
			} else if started {
				return nil
			}
		case <-t.C:
			return fmt.Errorf("keyboard macro did not finish within %s (started=%v)", limit, started)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Keys presses each combo in turn ("win+x", "ctrl+alt+Delete", "enter",
// "f11", "shift"), releasing everything after each one. holdMS is how long
// each combo is held (0 = default).
func Keys(ctx context.Context, d kvm.Device, combos []string, layout string, holdMS int) error {
	if holdMS <= 0 {
		holdMS = 50
	}
	if holdMS > math.MaxUint16 {
		return fmt.Errorf("hold %dms too large", holdMS)
	}
	// Character keys in combos need a layout; named keys don't, so only ask
	// the device when needed.
	needLayout := false
	for _, c := range combos {
		for _, p := range strings.Split(c, "+") {
			if len([]rune(p)) == 1 {
				needLayout = true
			}
		}
	}
	if layout == "" && !needLayout {
		layout = LayoutUS
	}
	layout, err := resolveLayout(ctx, d, layout)
	if err != nil {
		return err
	}
	var steps []macroStep
	for _, c := range combos {
		mod, keys, err := parseCombo(c, layout)
		if err != nil {
			return err
		}
		// Press modifiers first, then the keys, as a real keyboard does:
		// some targets (BIOS, Windows secure desktop) ignore a report that
		// sets both at once.
		if mod != 0 && len(keys) > 0 {
			steps = append(steps, macroStep{mod: mod, delay: 20})
		}
		steps = append(steps, macroStep{mod: mod, keys: keys, delay: uint16(holdMS)}, macroStep{delay: 50})
	}
	return runMacro(ctx, d, steps)
}

// Button is a mouse button bitmask as sent in pointer reports.
type Button uint8

// Mouse buttons.
const (
	ButtonLeft   Button = 1
	ButtonRight  Button = 2
	ButtonMiddle Button = 4
)

// Point is an absolute position. If Normalized, X and Y are 0..1 of the
// screen; otherwise they are pixels of a Width x Height screen.
type Point struct {
	X, Y          float64
	Normalized    bool
	Width, Height int // screen size in pixels; required when !Normalized
}

// absMax is the absolute pointer's logical maximum (hid_mouse_absolute.go).
const absMax = 32767

// toAbs maps p to the 0..32767 HID range.
func toAbs(p Point) (int32, int32, error) {
	fx, fy := p.X, p.Y
	if !p.Normalized {
		if p.Width <= 0 || p.Height <= 0 {
			return 0, 0, errors.New("screen size unknown")
		}
		if fx < 0 || fy < 0 || fx >= float64(p.Width) || fy >= float64(p.Height) {
			return 0, 0, fmt.Errorf("(%g,%g) is outside the %dx%d screen", fx, fy, p.Width, p.Height)
		}
		// Pixel centres, so the last pixel maps inside the range.
		fx = (fx + 0.5) / float64(p.Width)
		fy = (fy + 0.5) / float64(p.Height)
	} else if fx < 0 || fx > 1 || fy < 0 || fy > 1 {
		return 0, 0, fmt.Errorf("normalised (%g,%g) outside 0..1", fx, fy)
	}
	return int32(math.Round(fx * absMax)), int32(math.Round(fy * absMax)), nil
}

func pointer(d kvm.Device, x, y int32, b Button) error {
	m := make([]byte, 10)
	m[0] = msgPointerReport
	binary.BigEndian.PutUint32(m[1:5], uint32(x))
	binary.BigEndian.PutUint32(m[5:9], uint32(y))
	m[9] = byte(b)
	return d.SendHID(m)
}

// settle gives the device time to process queued hidrpc messages before the
// caller closes the session (which resets HID state).
func settle(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// MouseMove moves the absolute pointer.
func MouseMove(ctx context.Context, d kvm.Device, p Point) error {
	x, y, err := toAbs(p)
	if err != nil {
		return err
	}
	if err := pointer(d, x, y, 0); err != nil {
		return err
	}
	return settle(ctx, 100*time.Millisecond)
}

// MouseClick moves to p, presses and releases b (count times).
func MouseClick(ctx context.Context, d kvm.Device, p Point, b Button, count int) error {
	x, y, err := toAbs(p)
	if err != nil {
		return err
	}
	if count < 1 {
		count = 1
	}
	if err := pointer(d, x, y, 0); err != nil {
		return err
	}
	if err := settle(ctx, 50*time.Millisecond); err != nil {
		return err
	}
	for i := 0; i < count; i++ {
		if err := pointer(d, x, y, b); err != nil {
			return err
		}
		if err := settle(ctx, 60*time.Millisecond); err != nil {
			return err
		}
		if err := pointer(d, x, y, 0); err != nil {
			return err
		}
		if err := settle(ctx, 80*time.Millisecond); err != nil {
			return err
		}
	}
	return settle(ctx, 100*time.Millisecond)
}

// MouseScroll sends wheel detents; positive dy = up. hidrpc has a wheel
// message type but 0.5.9 does not handle it, so this uses the JSON-RPC
// wheelReport like the UI.
func MouseScroll(ctx context.Context, d kvm.Device, dy, dx int) error {
	step := func(v int) int {
		switch {
		case v > 0:
			return 1
		case v < 0:
			return -1
		}
		return 0
	}
	for dy != 0 || dx != 0 {
		sy, sx := step(dy), step(dx)
		if _, err := d.Call(ctx, "wheelReport", map[string]any{"wheelY": sy, "wheelX": sx}); err != nil {
			return err
		}
		dy -= sy
		dx -= sx
		if err := settle(ctx, 30*time.Millisecond); err != nil {
			return err
		}
	}
	return nil
}
