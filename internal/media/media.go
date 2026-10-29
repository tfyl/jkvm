// Package media drives the device's virtual media (USB CD-ROM / disk) and
// collects the device info shown by `jkvm info`.
package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/tfyl/jkvm/internal/kvm"
)

// Mode is the USB mass-storage presentation.
type Mode string

// Modes accepted by MountURL and MountFile.
const (
	ModeCDROM Mode = "CDROM"
	ModeDisk  Mode = "Disk"
)

// Status returns the mounted image (getVirtualMediaState, null when none),
// storage space and the files in device storage.
func Status(ctx context.Context, d kvm.Device) (json.RawMessage, error) {
	return gather(ctx, d, []string{"getVirtualMediaState", "getStorageSpace", "listStorageFiles", "getMassStorageMode"})
}

// MountURL mounts an HTTP(S) image via the device's own URL mount
// (mountWithHTTP: the device streams it over NBD; the server must support
// range requests). checkMountUrl runs first so a bad URL fails cleanly.
func MountURL(ctx context.Context, d kvm.Device, rawURL string, mode Mode) error {
	if err := ensureUnmounted(ctx, d); err != nil {
		return err
	}
	// An unusable URL is a successful call with usable=false, not an RPC error.
	raw, err := d.Call(ctx, "checkMountUrl", map[string]any{"url": rawURL})
	if err != nil {
		return err
	}
	var check struct {
		Usable bool   `json:"usable"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &check); err != nil {
		return fmt.Errorf("checkMountUrl: %w", err)
	}
	if !check.Usable {
		return fmt.Errorf("checkMountUrl: %s", check.Reason)
	}
	if _, err := d.Call(ctx, "mountWithHTTP", map[string]any{"url": rawURL, "mode": string(mode)}); err != nil {
		return err
	}
	return verifyMounted(ctx, d)
}

// MountFile uploads a local image to device storage (resuming partial
// uploads, skipping the upload when a same-size file is already there) and
// mounts it. progress may be nil.
func MountFile(ctx context.Context, d kvm.Device, path string, mode Mode, progress func(sent, total int64)) error {
	if err := ensureUnmounted(ctx, d); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	name := filepath.Base(path)

	existing, err := storedSize(ctx, d, name)
	if err != nil {
		return err
	}
	switch {
	case existing == st.Size():
		// already uploaded
	case existing >= 0:
		return fmt.Errorf("device storage already has %s with a different size (%d vs %d bytes); delete it with: jkvm rpc deleteStorageFile '{\"filename\":%q}'", name, existing, st.Size(), name)
	default:
		if err := upload(ctx, d, f, name, st.Size(), progress); err != nil {
			return err
		}
	}
	if _, err := d.Call(ctx, "mountWithStorage", map[string]any{"filename": name, "mode": string(mode)}); err != nil {
		return err
	}
	return verifyMounted(ctx, d)
}

// upload follows the UI: startStorageFileUpload returns an upload id and the
// size of any <name>.incomplete left by an earlier attempt; the remaining
// bytes go to POST /storage/upload?uploadId=... (the HTTP transport the UI
// also uses, faster than an upload_* data channel).
func upload(ctx context.Context, d kvm.Device, f *os.File, name string, size int64, progress func(sent, total int64)) error {
	raw, err := d.Call(ctx, "startStorageFileUpload", map[string]any{"filename": name, "size": size})
	if err != nil {
		return err
	}
	var start struct {
		AlreadyUploadedBytes int64  `json:"alreadyUploadedBytes"`
		DataChannel          string `json:"dataChannel"`
	}
	if err := json.Unmarshal(raw, &start); err != nil {
		return fmt.Errorf("startStorageFileUpload: %w", err)
	}
	if start.DataChannel == "" {
		return fmt.Errorf("startStorageFileUpload returned no upload id: %s", raw)
	}
	if start.AlreadyUploadedBytes > size {
		return fmt.Errorf("device holds a %d-byte partial %s.incomplete, larger than the %d-byte file", start.AlreadyUploadedBytes, name, size)
	}
	if _, err := f.Seek(start.AlreadyUploadedBytes, io.SeekStart); err != nil {
		return err
	}
	var body io.Reader = f
	if progress != nil {
		body = &progressReader{r: f, sent: start.AlreadyUploadedBytes, total: size, fn: progress}
	}
	u := d.BaseURL() + "/storage/upload?uploadId=" + url.QueryEscape(start.DataChannel)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, body)
	if err != nil {
		return err
	}
	req.ContentLength = size - start.AlreadyUploadedBytes
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upload: %s: %s", resp.Status, msg)
	}
	if progress != nil {
		fmt.Fprintln(os.Stderr)
	}
	// The device renames <name>.incomplete into place when the byte count
	// matches; confirm that happened.
	got, err := storedSize(ctx, d, name)
	if err != nil {
		return err
	}
	if got != size {
		return fmt.Errorf("upload finished but %s is not in device storage at %d bytes", name, size)
	}
	return nil
}

type progressReader struct {
	r           io.Reader
	sent, total int64
	last        time.Time
	fn          func(sent, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.sent += int64(n)
	if time.Since(p.last) > 500*time.Millisecond || err == io.EOF {
		p.fn(p.sent, p.total)
		p.last = time.Now()
	}
	return n, err
}

// storedSize returns the size of name in device storage, or -1 if absent.
func storedSize(ctx context.Context, d kvm.Device, name string) (int64, error) {
	raw, err := d.Call(ctx, "listStorageFiles", nil)
	if err != nil {
		return 0, err
	}
	var list struct {
		Files []struct {
			Filename string `json:"filename"`
			Size     int64  `json:"size"`
		} `json:"files"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return 0, fmt.Errorf("listStorageFiles: %w", err)
	}
	for _, f := range list.Files {
		if f.Filename == name {
			return f.Size, nil
		}
	}
	return -1, nil
}

func ensureUnmounted(ctx context.Context, d kvm.Device) error {
	raw, err := d.Call(ctx, "getVirtualMediaState", nil)
	if err != nil {
		return err
	}
	if string(raw) != "null" {
		return fmt.Errorf("virtual media already mounted (%s); run jkvm media unmount first", raw)
	}
	return nil
}

func verifyMounted(ctx context.Context, d kvm.Device) error {
	raw, err := d.Call(ctx, "getVirtualMediaState", nil)
	if err != nil {
		return err
	}
	if string(raw) == "null" {
		return errors.New("mount call succeeded but getVirtualMediaState reports nothing mounted")
	}
	fmt.Printf("mounted: %s\n", raw)
	return nil
}

// Unmount ejects the current image.
func Unmount(ctx context.Context, d kvm.Device) error {
	_, err := d.Call(ctx, "unmountImage", nil)
	return err
}

// Info gathers device id, firmware version, video state, USB/HID state.
func Info(ctx context.Context, d kvm.Device) (json.RawMessage, error) {
	return gather(ctx, d, []string{
		"getDeviceID", "getLocalVersion", "getVideoState", "getUSBState",
		"getUsbEmulationState", "getUsbConfig", "getUsbDevices", "getKeyboardLayout",
		"getKeyboardLedState", "getVirtualMediaState", "getActiveExtension",
	})
}

// gather calls each read-only method and returns {"method": result}. A
// method that errors is reported inline rather than failing the whole call.
func gather(ctx context.Context, d kvm.Device, methods []string) (json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	ok := 0
	for _, m := range methods {
		raw, err := d.Call(ctx, m, nil)
		if err != nil {
			if ctx.Err() != nil {
				return nil, err
			}
			b, _ := json.Marshal(map[string]string{"error": err.Error()})
			out[m] = b
			continue
		}
		out[m] = raw
		ok++
	}
	if ok == 0 {
		return nil, errors.New("every RPC call failed")
	}
	return json.Marshal(out)
}
