package fixture

import (
	"os"
	"path/filepath"

	"github.com/pion/rtp"

	"mediad/internal/media"
)

// RTPSpec describes one RTP capture fixture.
type RTPSpec struct {
	Path      string
	ClockRate uint32
	// Timestamps are raw 32-bit RTP timestamps, so callers can express wrap.
	Timestamps []uint32
	Payloads   [][]byte
	SSRC       uint32
}

// WriteRTP serializes a capture in the kernel's RTP1 container format.
func WriteRTP(spec RTPSpec) error {
	if err := os.MkdirAll(filepath.Dir(spec.Path), 0o755); err != nil {
		return err
	}
	pkts := make([]*rtp.Packet, len(spec.Timestamps))
	for i, ts := range spec.Timestamps {
		pkts[i] = &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: uint16(i),
				Timestamp:      ts,
				SSRC:           spec.SSRC,
				Marker:         i == len(spec.Timestamps)-1,
			},
			Payload: spec.Payloads[i],
		}
	}
	f, err := os.Create(spec.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	return media.WriteRTPCapture(f, spec.ClockRate, pkts)
}

// WrapTimestamps builds a timestamp sequence that crosses the 32-bit
// boundary: it starts near 2^32 and advances by step each packet.
func WrapTimestamps(start uint32, step uint32, count int) []uint32 {
	out := make([]uint32, count)
	ts := start
	for i := range out {
		out[i] = ts
		ts += step
	}
	return out
}
