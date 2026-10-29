// Package kvmtest provides a fake kvm.Device for tests.
package kvmtest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"github.com/pion/webrtc/v4"

	"github.com/tfyl/jkvm/internal/kvm"
)

// Device is a fake kvm.Device. RPC answers come from Results (a missing
// method is a "Method not found" error); hidrpc messages are recorded in
// Sent. OnHID, if set, is called for each sent message and may push replies
// with Reply.
type Device struct {
	Results map[string]json.RawMessage
	OnHID   func(d *Device, msg []byte)
	URL     string

	mu    sync.Mutex
	calls []Call
	sent  [][]byte

	events chan kvm.Event
	hidIn  chan []byte
}

// Call is one recorded RPC.
type Call struct {
	Method string
	Params map[string]any
}

var _ kvm.Device = (*Device)(nil)

// New returns a fake whose RPC methods answer with results.
func New(results map[string]string) *Device {
	d := &Device{
		Results: map[string]json.RawMessage{},
		URL:     "http://jetkvm.test",
		events:  make(chan kvm.Event, 16),
		hidIn:   make(chan []byte, 64),
	}
	for m, r := range results {
		d.Results[m] = json.RawMessage(r)
	}
	return d
}

// Call implements kvm.RPC.
func (d *Device) Call(_ context.Context, method string, params map[string]any) (json.RawMessage, error) {
	d.mu.Lock()
	d.calls = append(d.calls, Call{method, params})
	r, ok := d.Results[method]
	d.mu.Unlock()
	if !ok {
		return nil, &kvm.RPCError{Code: -32601, Message: "Method not found"}
	}
	return r, nil
}

// Calls returns the RPCs made so far.
func (d *Device) Calls() []Call {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]Call(nil), d.calls...)
}

// Events implements kvm.RPC.
func (d *Device) Events() <-chan kvm.Event { return d.events }

// SendHID implements kvm.HID.
func (d *Device) SendHID(msg []byte) error {
	d.mu.Lock()
	d.sent = append(d.sent, append([]byte(nil), msg...))
	d.mu.Unlock()
	if d.OnHID != nil {
		d.OnHID(d, msg)
	}
	return nil
}

// Sent returns the hidrpc messages sent so far.
func (d *Device) Sent() [][]byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([][]byte(nil), d.sent...)
}

// Reply queues a hidrpc message from the device.
func (d *Device) Reply(msg []byte) { d.hidIn <- msg }

// HIDMessages implements kvm.HID.
func (d *Device) HIDMessages() <-chan []byte { return d.hidIn }

// VideoTrack implements kvm.Video; the fake has no video.
func (d *Device) VideoTrack(context.Context) (*webrtc.TrackRemote, error) {
	return nil, errors.New("kvmtest: no video")
}

// RequestKeyframe implements kvm.Video.
func (d *Device) RequestKeyframe() error { return errors.New("kvmtest: no video") }

// OpenChannel implements kvm.Channels.
func (d *Device) OpenChannel(context.Context, string, *webrtc.DataChannelInit) (io.ReadWriteCloser, error) {
	return nil, errors.New("kvmtest: no channels")
}

// BaseURL implements kvm.Device.
func (d *Device) BaseURL() string { return d.URL }
