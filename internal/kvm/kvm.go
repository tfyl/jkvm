// Package kvm holds the interfaces shared between the session (transport)
// and the feature packages (hid, video, media). Feature packages depend only
// on these interfaces, never on the session implementation.
package kvm

import (
	"context"
	"encoding/json"
	"io"

	"github.com/pion/webrtc/v4"
)

// RPC is the JSON-RPC 2.0 client on the "rpc" data channel.
type RPC interface {
	// Call sends {"jsonrpc":"2.0","method":method,"params":params,"id":n} and
	// waits for the matching response. params may be nil. A JSON-RPC error
	// response is returned as *RPCError.
	Call(ctx context.Context, method string, params map[string]any) (json.RawMessage, error)
	// Events returns server-initiated notifications (no id), e.g.
	// "videoInputState", "usbState", "otherSessionConnected".
	Events() <-chan Event
}

// Event is a JSON-RPC notification pushed by the device.
type Event struct {
	Method string
	Params json.RawMessage
}

// RPCError is a JSON-RPC error object returned by the device.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if len(e.Data) > 0 {
		return e.Message + ": " + string(e.Data)
	}
	return e.Message
}

// HID is the binary "hidrpc" data channel (reliable, ordered). The session
// has already completed the hidrpc handshake (0x01 0x01) before handing it out.
type HID interface {
	// SendHID writes one hidrpc message: type byte followed by payload.
	SendHID(msg []byte) error
	// HIDMessages returns messages the device sends back (LED state 0x32,
	// keys-down state 0x33, macro state 0x34).
	HIDMessages() <-chan []byte
}

// Video gives access to the device's video track.
type Video interface {
	// VideoTrack blocks until the remote video track arrives.
	VideoTrack(ctx context.Context) (*webrtc.TrackRemote, error)
	// RequestKeyframe sends an RTCP PLI for the video track. The device only
	// emits an IDR frame on feedback, so call this after attaching a reader.
	RequestKeyframe() error
}

// Channels opens extra data channels after connect (e.g. upload channels,
// "terminal", "serial"). Renegotiation is handled by the session.
type Channels interface {
	OpenChannel(ctx context.Context, label string, init *webrtc.DataChannelInit) (io.ReadWriteCloser, error)
}

// Device bundles everything a command needs. BaseURL is the device's HTTP
// origin, e.g. "http://jetkvm.local", for plain HTTP endpoints such as
// /device or /storage/upload.
type Device interface {
	RPC
	HID
	Video
	Channels
	BaseURL() string
}
