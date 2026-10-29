package video

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// decodePNG runs the system ffmpeg over an Annex-B H.264 stream and returns
// the last decoded picture as PNG.
func decodePNG(ctx context.Context, annexB []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "h264", "-i", "pipe:0", "-f", "image2pipe", "-c:v", "png", "pipe:1")
	cmd.Stdin = bytes.NewReader(annexB)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	pngs := splitPNGs(out.Bytes())
	if len(pngs) == 0 {
		return nil, fmt.Errorf("ffmpeg decoded no frame: %s", strings.TrimSpace(stderr.String()))
	}
	return pngs[len(pngs)-1], nil
}

var pngMagic = []byte("\x89PNG\r\n\x1a\n")

// splitPNGs cuts a concatenation of PNG files (image2pipe output) at each
// IEND chunk. A truncated trailing image is dropped.
func splitPNGs(b []byte) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		if !bytes.HasPrefix(b, pngMagic) {
			return out
		}
		off := len(pngMagic)
		for {
			if off+8 > len(b) {
				return out
			}
			n := int(binary.BigEndian.Uint32(b[off:]))
			typ := string(b[off+4 : off+8])
			off += 12 + n // length, type, data, CRC
			if off > len(b) || n < 0 {
				return out
			}
			if typ == "IEND" {
				break
			}
		}
		out = append(out, b[:off])
		b = b[off:]
	}
	return out
}

var errNoFFmpeg = errors.New("ffmpeg not found in PATH (needed to decode video)")

func checkFFmpeg() error {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return errNoFFmpeg
	}
	return nil
}
