// Package evidence renders offline, dependency-free evidence of timeline
// reconstruction: PPM frames hashed with SHA-256 and audio level statistics.
// No GUI, network access or lossy transcoding is involved.
package evidence

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
)

// Frame is the parsed view of one synthetic video sample payload.
type Frame struct {
	Width  uint16
	Height uint16
	Seed   byte
	// Pixels are RGB triples in row-major order, exactly Width*Height*3.
	Pixels []byte
}

// magic prefix produced by internal/synthetic framePayload.
var frameMagic = [4]byte{'F', 'R', 'M', 0}

// DecodeFrame parses an 8-byte-header synthetic video sample.
func DecodeFrame(payload []byte) (*Frame, error) {
	if len(payload) < 8 {
		return nil, errors.New("frame payload shorter than header")
	}
	for i, m := range frameMagic {
		if payload[i] != m {
			return nil, fmt.Errorf("bad frame magic at byte %d", i)
		}
	}
	w := binary.BigEndian.Uint16(payload[4:])
	h := binary.BigEndian.Uint16(payload[6:])
	need := int(w) * int(h) * 3
	if len(payload)-8 < need {
		return nil, fmt.Errorf("truncated frame: have %d pixel bytes, need %d", len(payload)-8, need)
	}
	pix := make([]byte, need)
	copy(pix, payload[8:8+need])
	return &Frame{Width: w, Height: h, Seed: payload[7], Pixels: pix}, nil
}

// FrameEvidence is the deterministic rendering result of one frame.
type FrameEvidence struct {
	Index       int    `json:"index"`
	SHA256      string `json:"sha256"`
	PixelMeanR  float64 `json:"mean_r"`
	PixelMeanG  float64 `json:"mean_g"`
	PixelMeanB  float64 `json:"mean_b"`
	PPMBytes    int    `json:"ppm_bytes"`
}

// RenderFrame decodes, verifies, re-encodes a binary PPM (P6) and hashes it.
// The hash covers the exact emitted byte sequence, so fixed seeds reproduce.
func RenderFrame(index int, payload []byte) (*FrameEvidence, error) {
	f, err := DecodeFrame(payload)
	if err != nil {
		return nil, err
	}
	ppm := encodePPM(f)
	sum := sha256.Sum256(ppm)
	var sr, sg, sb float64
	n := float64(len(f.Pixels) / 3)
	for i := 0; i < len(f.Pixels); i += 3 {
		sr += float64(f.Pixels[i])
		sg += float64(f.Pixels[i+1])
		sb += float64(f.Pixels[i+2])
	}
	return &FrameEvidence{
		Index: index, SHA256: hex.EncodeToString(sum[:]),
		PixelMeanR: sr / n, PixelMeanG: sg / n, PixelMeanB: sb / n,
		PPMBytes: len(ppm),
	}, nil
}

func encodePPM(f *Frame) []byte {
	header := fmt.Sprintf("P6\n%d %d\n255\n", f.Width, f.Height)
	out := make([]byte, 0, len(header)+len(f.Pixels))
	out = append(out, header...)
	out = append(out, f.Pixels...)
	return out
}
