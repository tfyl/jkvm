// Package session connects to a JetKVM the way its web UI does: websocket
// signaling at /webrtc/signaling/client, one pion PeerConnection, the "rpc"
// and "hidrpc" data channels and a receive-only video transceiver. It
// implements kvm.Device.
package session

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"

	"github.com/tfyl/jkvm/internal/kvm"
)

// Options configures Dial.
type Options struct {
	BaseURL  string // e.g. "http://jetkvm.local"
	Password string // "" for authMode noPassword
	// Video adds a recvonly video transceiver; commands that never look at
	// the screen leave it off so the device doesn't start encoding.
	Video   bool
	Timeout time.Duration // connect timeout; 0 = 20s
	Verbose bool          // log signaling / ICE to stderr
}

// Session is a live connection. It satisfies kvm.Device.
type Session struct {
	opt  Options
	log  *log.Logger
	lock *os.File

	pc  *webrtc.PeerConnection
	ws  *websocket.Conn
	rpc *webrtc.DataChannel
	hid *webrtc.DataChannel

	mu      sync.Mutex
	nextID  int
	pending map[int]chan rpcResponse
	events  chan kvm.Event
	hidIn   chan []byte
	closed  chan struct{}
	closeMu sync.Once
	failErr error

	trackCh chan *webrtc.TrackRemote
	track   *webrtc.TrackRemote
}

var _ kvm.Device = (*Session)(nil)

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *kvm.RPCError   `json:"error"`
	ID     *int            `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// Dial takes the per-device lock (only one jkvm process talks to a device at
// a time, since a new WebRTC session kicks the previous one), signals, and
// waits until the rpc and hidrpc channels are open and the hidrpc handshake
// is done.
func Dial(ctx context.Context, opt Options) (*Session, error) {
	if opt.Timeout == 0 {
		opt.Timeout = 20 * time.Second
	}
	lock, err := lockDevice(ctx, opt.BaseURL)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, opt.Timeout)
	defer cancel()
	// The device occasionally closes the signaling socket right after a
	// previous session went away; a fresh attempt then succeeds.
	for attempt := 1; ; attempt++ {
		s := newSession(opt)
		err := s.connect(cctx)
		if err == nil {
			s.lock = lock
			return s, nil
		}
		s.Close()
		if cctx.Err() != nil && ctx.Err() == nil {
			lock.Close()
			return nil, fmt.Errorf("connect to %s timed out after %s: %w (run with -v for ICE details)", opt.BaseURL, opt.Timeout, err)
		}
		var retry retryable
		if attempt >= 3 || !errors.As(err, &retry) || ctx.Err() != nil {
			lock.Close()
			return nil, fmt.Errorf("connect to %s: %w", opt.BaseURL, err)
		}
		s.log.Printf("attempt %d: %v; retrying", attempt, err)
		time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
	}
}

// retryable marks connect errors worth another attempt.
type retryable struct{ error }

func (r retryable) Unwrap() error { return r.error }

func newSession(opt Options) *Session {
	s := &Session{
		opt:     opt,
		log:     log.New(io.Discard, "", 0),
		pending: map[int]chan rpcResponse{},
		events:  make(chan kvm.Event, 64),
		hidIn:   make(chan []byte, 64),
		closed:  make(chan struct{}),
		trackCh: make(chan *webrtc.TrackRemote, 1),
	}
	if opt.Verbose {
		s.log = log.New(os.Stderr, "jkvm: ", log.Lmicroseconds)
	}
	return s
}

// lockDevice serialises jkvm processes per device with flock on a file in
// the temp dir. The lock is released when the process exits.
func lockDevice(ctx context.Context, base string) (*os.File, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("bad host %q: %w", base, err)
	}
	name := filepath.Join(os.TempDir(), "jkvm-"+strings.NewReplacer(":", "_", "/", "_").Replace(u.Host)+".lock")
	f, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o666)
	if err != nil {
		return nil, fmt.Errorf("open device lock: %w", err)
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, fmt.Errorf("lock %s: %w", name, err)
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, fmt.Errorf("another jkvm process holds %s: %w", name, ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (s *Session) connect(ctx context.Context) error {
	header := http.Header{}
	if s.opt.Password != "" {
		cookie, err := login(ctx, s.opt.BaseURL, s.opt.Password)
		if err != nil {
			return err
		}
		header.Set("Cookie", cookie)
	}

	wsURL := "ws" + strings.TrimPrefix(s.opt.BaseURL, "http") + "/webrtc/signaling/client"
	ws, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: header}) //nolint:bodyclose // Dial closes it

	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return errors.New("signaling: 401 unauthorized (set -password / JKVM_PASSWORD)")
		}
		return fmt.Errorf("signaling websocket: %w", err)
	}
	ws.SetReadLimit(1 << 20)
	s.ws = ws

	// The device speaks first with its version; the UI waits for this
	// before creating the peer connection.
	var meta struct {
		Type string `json:"type"`
		Data struct {
			DeviceVersion string `json:"deviceVersion"`
		} `json:"data"`
	}
	if err := readJSON(ctx, ws, &meta); err != nil {
		return retryable{fmt.Errorf("signaling: waiting for device-metadata: %w", err)}
	}
	s.log.Printf("device-metadata: version %s", meta.Data.DeviceVersion)

	if err := s.newPeer(); err != nil {
		return err
	}

	opened := make(chan struct{}, 2)
	s.rpc, err = s.pc.CreateDataChannel("rpc", nil)
	if err != nil {
		return err
	}
	s.rpc.OnOpen(func() { opened <- struct{}{} })
	s.rpc.OnMessage(s.onRPC)

	s.hid, err = s.pc.CreateDataChannel("hidrpc", nil)
	if err != nil {
		return err
	}
	handshake := make(chan struct{})
	var hsOnce sync.Once
	s.hid.OnOpen(func() { opened <- struct{}{} })
	s.hid.OnMessage(func(m webrtc.DataChannelMessage) {
		if len(m.Data) >= 1 && m.Data[0] == 0x01 {
			hsOnce.Do(func() { close(handshake) })
			return
		}
		select {
		case s.hidIn <- append([]byte(nil), m.Data...):
		default:
		}
	})

	if s.opt.Video {
		if _, err := s.pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo,
			webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
			return err
		}
	}

	// Trickle our candidates like the UI does.
	s.pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			s.log.Printf("local ICE gathering complete")
			return
		}
		init := c.ToJSON()
		s.log.Printf("local candidate: %s", init.Candidate)
		if err := writeJSON(context.Background(), ws, map[string]any{"type": "new-ice-candidate", "data": init}); err != nil {
			s.log.Printf("send candidate: %v", err)
		}
	})

	offer, err := s.pc.CreateOffer(nil)
	if err != nil {
		return err
	}
	if err := s.pc.SetLocalDescription(offer); err != nil {
		return err
	}
	sdJSON, err := json.Marshal(s.pc.LocalDescription())
	if err != nil {
		return err
	}
	if err := writeJSON(ctx, ws, map[string]any{
		"type": "offer",
		"data": map[string]string{"sd": base64.StdEncoding.EncodeToString(sdJSON)},
	}); err != nil {
		return fmt.Errorf("signaling: send offer: %w", err)
	}

	answered := make(chan error, 1)
	go s.readSignaling(ws, answered)
	select {
	case err := <-answered:
		if err != nil {
			return err
		}
	case <-ctx.Done():
		return fmt.Errorf("no answer from device: %w", ctx.Err())
	}

	for i := 0; i < 2; i++ {
		select {
		case <-opened:
		case <-s.closed:
			return s.err("peer connection closed")
		case <-ctx.Done():
			return fmt.Errorf("data channels did not open (ICE state %s): %w", s.pc.ICEConnectionState(), ctx.Err())
		}
	}

	// hidrpc handshake: [0x01 type, 0x01 version]; the UI retries it until
	// the device echoes it back.
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := s.hid.Send([]byte{0x01, 0x01}); err != nil {
			return fmt.Errorf("hidrpc handshake: %w", err)
		}
		select {
		case <-handshake:
			s.log.Printf("connected; hidrpc handshake done")
			return nil
		case <-tick.C:
		case <-ctx.Done():
			return fmt.Errorf("hidrpc handshake: %w", ctx.Err())
		}
	}
}

func (s *Session) newPeer() error {
	// Offer H.264 only: the firmware picks H.265 whenever the offer lists it,
	// and H.264 depacketises and decodes everywhere.
	me := &webrtc.MediaEngine{}
	if err := me.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeH264, ClockRate: 90000,
			SDPFmtpLine:  "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
			RTCPFeedback: []webrtc.RTCPFeedback{{Type: "nack"}, {Type: "nack", Parameter: "pli"}, {Type: "ccm", Parameter: "fir"}},
		},
		PayloadType: 102,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		return err
	}
	se := webrtc.SettingEngine{}
	api := webrtc.NewAPI(webrtc.WithMediaEngine(me), webrtc.WithSettingEngine(se))
	// No ICE servers: on the LAN the UI uses none either (it only passes
	// cloud-provided ones when connecting via app.jetkvm.com).
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return err
	}
	s.pc = pc
	pc.OnICEConnectionStateChange(func(st webrtc.ICEConnectionState) {
		s.log.Printf("ICE state: %s", st)
		if st == webrtc.ICEConnectionStateFailed {
			s.fail(errors.New("ICE failed"))
		}
	})
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		s.log.Printf("peer state: %s", st)
		if st == webrtc.PeerConnectionStateFailed || st == webrtc.PeerConnectionStateClosed {
			s.fail(fmt.Errorf("peer connection %s", st))
		}
	})
	pc.OnTrack(func(t *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		s.log.Printf("track: %s %s ssrc=%d", t.Kind(), t.Codec().MimeType, t.SSRC())
		if t.Kind() == webrtc.RTPCodecTypeVideo {
			select {
			case s.trackCh <- t:
			default:
			}
		}
	})
	return nil
}

func (s *Session) readSignaling(ws *websocket.Conn, answered chan<- error) {
	gotAnswer := false
	var early []webrtc.ICECandidateInit
	for {
		typ, data, err := ws.Read(context.Background())
		if err != nil {
			if !gotAnswer {
				answered <- retryable{fmt.Errorf("signaling closed before answer: %w", err)}
			}
			return
		}
		if typ != websocket.MessageText || bytes.Equal(data, []byte("pong")) {
			continue
		}
		var msg struct {
			Type  string          `json:"type"`
			Data  json.RawMessage `json:"data"`
			Error json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		switch {
		case len(msg.Error) > 0 && !gotAnswer:
			answered <- fmt.Errorf("device rejected offer: %s", msg.Error)
			return
		case msg.Type == "answer" && !gotAnswer:
			var b64 string
			var sd webrtc.SessionDescription
			if err := json.Unmarshal(msg.Data, &b64); err != nil {
				answered <- fmt.Errorf("bad answer: %w", err)
				return
			}
			raw, err := base64.StdEncoding.DecodeString(b64)
			if err == nil {
				err = json.Unmarshal(raw, &sd)
			}
			if err == nil {
				err = s.pc.SetRemoteDescription(sd)
			}
			if err != nil {
				answered <- fmt.Errorf("apply answer: %w", err)
				return
			}
			s.log.Printf("answer applied")
			gotAnswer = true
			for _, c := range early {
				s.addCandidate(c)
			}
			early = nil
			answered <- nil
		case msg.Type == "new-ice-candidate":
			var c webrtc.ICECandidateInit
			if err := json.Unmarshal(msg.Data, &c); err != nil || c.Candidate == "" {
				continue
			}
			if gotAnswer {
				s.addCandidate(c)
			} else {
				early = append(early, c)
			}
		}
	}
}

func (s *Session) addCandidate(c webrtc.ICECandidateInit) {
	s.log.Printf("remote candidate: %s", c.Candidate)
	if err := s.pc.AddICECandidate(c); err != nil {
		s.log.Printf("add remote candidate: %v", err)
	}
}

func (s *Session) fail(err error) {
	s.closeMu.Do(func() {
		s.mu.Lock()
		s.failErr = err
		s.mu.Unlock()
		close(s.closed)
	})
}

func (s *Session) err(what string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failErr != nil {
		return fmt.Errorf("%s: %w", what, s.failErr)
	}
	return errors.New(what)
}

// Close tears down the peer connection and releases the device lock.
func (s *Session) Close() error {
	s.fail(errors.New("session closed"))
	if s.pc != nil {
		_ = s.pc.Close()
	}
	if s.ws != nil {
		_ = s.ws.Close(websocket.StatusNormalClosure, "")
	}
	if s.lock != nil {
		_ = s.lock.Close()
		s.lock = nil
	}
	return nil
}

// BaseURL implements kvm.Device.
func (s *Session) BaseURL() string { return s.opt.BaseURL }

func (s *Session) onRPC(m webrtc.DataChannelMessage) {
	var r rpcResponse
	if err := json.Unmarshal(m.Data, &r); err != nil {
		s.log.Printf("bad rpc message: %v", err)
		return
	}
	if r.ID == nil {
		if r.Method == "" {
			return
		}
		select {
		case s.events <- kvm.Event{Method: r.Method, Params: r.Params}:
		default:
		}
		return
	}
	s.mu.Lock()
	ch := s.pending[*r.ID]
	delete(s.pending, *r.ID)
	s.mu.Unlock()
	if ch != nil {
		ch <- r
	}
}

// Call implements kvm.RPC.
func (s *Session) Call(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	ch := make(chan rpcResponse, 1)
	s.pending[id] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}()

	req := map[string]any{"jsonrpc": "2.0", "method": method, "id": id}
	if params != nil {
		req["params"] = params
	}
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if err := s.rpc.SendText(string(b)); err != nil {
		return nil, fmt.Errorf("rpc %s: %w", method, err)
	}
	select {
	case r := <-ch:
		if r.Error != nil {
			return nil, fmt.Errorf("rpc %s: %w", method, r.Error)
		}
		if r.Result == nil {
			return json.RawMessage("null"), nil
		}
		return r.Result, nil
	case <-s.closed:
		return nil, s.err("rpc " + method)
	case <-ctx.Done():
		return nil, fmt.Errorf("rpc %s: %w", method, ctx.Err())
	}
}

// Events implements kvm.RPC.
func (s *Session) Events() <-chan kvm.Event { return s.events }

// SendHID implements kvm.HID.
func (s *Session) SendHID(msg []byte) error { return s.hid.Send(msg) }

// HIDMessages implements kvm.HID.
func (s *Session) HIDMessages() <-chan []byte { return s.hidIn }

// VideoTrack implements kvm.Video.
func (s *Session) VideoTrack(ctx context.Context) (*webrtc.TrackRemote, error) {
	if !s.opt.Video {
		return nil, errors.New("session was dialled without video")
	}
	s.mu.Lock()
	t := s.track
	s.mu.Unlock()
	if t != nil {
		return t, nil
	}
	select {
	case t := <-s.trackCh:
		s.mu.Lock()
		s.track = t
		s.mu.Unlock()
		return t, nil
	case <-s.closed:
		return nil, s.err("waiting for video track")
	case <-ctx.Done():
		return nil, fmt.Errorf("no video track: %w", ctx.Err())
	}
}

// RequestKeyframe implements kvm.Video.
func (s *Session) RequestKeyframe() error {
	s.mu.Lock()
	t := s.track
	s.mu.Unlock()
	if t == nil {
		return errors.New("no video track yet")
	}
	return s.pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(t.SSRC())}})
}

// OpenChannel implements kvm.Channels. SCTP is already up, so new channels
// need no renegotiation.
func (s *Session) OpenChannel(ctx context.Context, label string, init *webrtc.DataChannelInit) (io.ReadWriteCloser, error) {
	dc, err := s.pc.CreateDataChannel(label, init)
	if err != nil {
		return nil, err
	}
	c := &channel{dc: dc, in: make(chan []byte, 256), done: make(chan struct{})}
	open := make(chan struct{})
	dc.OnOpen(func() { close(open) })
	dc.OnMessage(func(m webrtc.DataChannelMessage) {
		select {
		case c.in <- append([]byte(nil), m.Data...):
		case <-c.done:
		}
	})
	dc.OnClose(func() { c.closeOnce.Do(func() { close(c.done) }) })
	select {
	case <-open:
		return c, nil
	case <-ctx.Done():
		_ = dc.Close()
		return nil, fmt.Errorf("open channel %q: %w", label, ctx.Err())
	}
}

type channel struct {
	dc        *webrtc.DataChannel
	in        chan []byte
	buf       []byte
	done      chan struct{}
	closeOnce sync.Once
}

func (c *channel) Read(p []byte) (int, error) {
	if len(c.buf) == 0 {
		select {
		case b := <-c.in:
			c.buf = b
		case <-c.done:
			return 0, io.EOF
		}
	}
	n := copy(p, c.buf)
	c.buf = c.buf[n:]
	return n, nil
}

func (c *channel) Write(p []byte) (int, error) {
	if err := c.dc.Send(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *channel) Close() error { return c.dc.Close() }

func login(ctx context.Context, base, password string) (string, error) {
	body, _ := json.Marshal(map[string]string{"password": password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/auth/login-local", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("login: %w", err)
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("login: %s: %s", resp.Status, bytes.TrimSpace(msg))
	}
	for _, c := range resp.Cookies() {
		if c.Name == "authToken" {
			return "authToken=" + c.Value, nil
		}
	}
	return "", errors.New("login: no authToken cookie in response")
}

func readJSON(ctx context.Context, ws *websocket.Conn, v any) error {
	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			return err
		}
		if typ == websocket.MessageText && !bytes.Equal(data, []byte("pong")) {
			return json.Unmarshal(data, v)
		}
	}
}

func writeJSON(ctx context.Context, ws *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return ws.Write(ctx, websocket.MessageText, b)
}
