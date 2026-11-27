// Package fixture generates all synthetic media used by tests and the
// acceptance command. No external files are referenced; every fixture is
// produced deterministically from integer parameters so repeatable runs
// yield identical index digests.
package fixture

import (
	"os"
	"path/filepath"

	"github.com/pion/rtp"
)

// Kind identifies a generated scenario.
type Kind string

const (
	KindNormal     Kind = "normal"
	KindWrap       Kind = "wrap"
	KindDrift      Kind = "drift"
	KindVFR        Kind = "vfr"
	KindDupReorder Kind = "dup_reorder"
	KindBurstLoss  Kind = "burst_loss"
	KindTruncated  Kind = "truncated"
)

// Spec configures one generated RTP/audio fixture.
type Spec struct {
	Kind        Kind
	SSRC        uint32
	Packets     int
	ClockRate   uint32 // RTP timestamp clock (Hz)
	PayloadSize int    // PCM16 mono bytes per packet
	WindowCap   int
	// WrapAt forces a 32-bit RTP timestamp wrap near this packet index.
	WrapAt int
	// DriftPPM skews remote timestamps by this many parts per million.
	DriftPPM float64
	// DropAt / DropCount remove a contiguous burst of packets.
	DropAt, DropCount int
	// TruncateAt cuts the serialized capture file at this byte position.
	TruncateAt int
}

// DefaultSpec returns a coherent baseline spec (1s of 8kHz-ish mono PCM in
// 20ms packets, 50 packets).
func DefaultSpec(k Kind) Spec {
	return Spec{
		Kind: k, SSRC: 0x4711C0DE, Packets: 50, ClockRate: 8000,
		PayloadSize: 320, WindowCap: 16,
		WrapAt: 40, DriftPPM: 0, DropAt: -1, DropCount: 0, TruncateAt: -1,
	}
}

// Packet is a generated RTP packet with its intended timeline position.
type Packet struct {
	RTP       *rtp.Packet
	Seq       uint16
	SendOrder int // position in which it is delivered (after dup/reorder/loss)
}

// Build produces the ordered delivery list for the spec, including any
// duplicates, reordering, burst loss and timestamp wrap/drift.
func Build(s Spec) []*rtp.Packet {
	pkts := make([]*rtp.Packet, 0, s.Packets+4)
	tsStep := uint32(s.ClockRate / 50) // 20 ms per packet
	for i := 0; i < s.Packets; i++ {
		if s.Kind == KindBurstLoss && i >= s.DropAt && i < s.DropAt+s.DropCount {
			continue
		}
		ts := uint32(int64(uint64(i)*uint64(tsStep)) + int64(float64(i)*s.DriftPPM/1e6*float64(s.ClockRate)))
		// Force a timestamp wrap by starting the clock near 2^32 for wrap cases.
		if s.Kind == KindWrap {
			ts = wrapTimestamp(i, int(tsStep))
		}
		p := &rtp.Packet{
			Header: rtp.Header{
				Version: 2, PayloadType: 96, SSRC: s.SSRC,
				SequenceNumber: uint16(i), Timestamp: ts, Marker: i == s.Packets-1,
			},
			Payload: make([]byte, s.PayloadSize),
		}
		for b := range p.Payload {
			p.Payload[b] = byte(i*7 + b)
		}
		pkts = append(pkts, p)
	}
	switch s.Kind {
	case KindDupReorder:
		// Multiple delayed packets produce several recoverable reorder holes
		// plus one duplicate; all are inside the reorder window.
		pkts = applyDupReorder(pkts)
	case KindBurstLoss:
		// Genuinely dropped packets are never re-inserted, so they remain
		// unrecoverable; the window must report them without fabricating
		// audio.
	case KindVFR:
		applyVFRTimestamps(pkts, tsStep)
	}
	return pkts
}

// wrapTimestamp places timestamps just below the 32-bit boundary so the
// sequence wraps exactly once across the generated stream.
func wrapTimestamp(i, step int) uint32 {
	const nearMax = uint64(1) << 32
	base := nearMax - uint64(10*step)
	return uint32((base + uint64(i)*uint64(step)) % nearMax)
}

// applyDupReorder delays several packets within the reorder window and adds
// one duplicate. Every delayed packet's hole is later filled, so each counts
// as a recoverable reordering event, and the duplicate is still inside the
// window when re-delivered.
func applyDupReorder(pkts []*rtp.Packet) []*rtp.Packet {
	if len(pkts) < 12 {
		return pkts
	}
	out := applyManyLate(pkts, []int{4, 8, 12})
	// Duplicate packet 2 and re-deliver it three positions later, well within
	// the window, so the window classifies it as a duplicate rather than late.
	dup := *pkts[2]
	dup.Payload = append([]byte(nil), pkts[2].Payload...)
	insertAt := 6
	if insertAt >= len(out) {
		insertAt = len(out)
	}
	out = append(out[:insertAt], append([]*rtp.Packet{&dup}, out[insertAt:]...)...)
	return out
}

// applyManyLate delays each listed packet by three delivery positions.
func applyManyLate(pkts []*rtp.Packet, positions []int) []*rtp.Packet {
	out := append([]*rtp.Packet(nil), pkts...)
	for _, idx := range positions {
		if idx < 0 || idx+4 >= len(out) {
			continue
		}
		late := out[idx]
		out = append(out[:idx], out[idx+1:]...)
		insertAt := idx + 3
		if insertAt >= len(out) {
			insertAt = len(out)
		}
		out = append(out[:insertAt], append([]*rtp.Packet{late}, out[insertAt:]...)...)
	}
	return out
}

// applyVFRTimestamps gives packets non-uniform PTS deltas while keeping
// sequence numbers continuous.
func applyVFRTimestamps(pkts []*rtp.Packet, baseStep uint32) {
	if len(pkts) == 0 {
		return
	}
	var ts uint32
	for i, p := range pkts {
		p.Timestamp = ts
		// Alternate 1x and 1.5x spacing: always forward, never uniform.
		if i%2 == 0 {
			ts += baseStep
		} else {
			ts += baseStep + baseStep/2
		}
	}
}

// WriteCapture serializes packets back-to-back with a small framing header
// (2-byte length prefix) into dir/name and returns the path.
func WriteCapture(dir, name string, pkts []*rtp.Packet, truncateAt int) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	var raw []byte
	for _, p := range pkts {
		b, err := p.Marshal()
		if err != nil {
			return "", err
		}
		hdr := []byte{byte(len(b) >> 8), byte(len(b))}
		raw = append(raw, hdr...)
		raw = append(raw, b...)
	}
	if truncateAt >= 0 && truncateAt < len(raw) {
		raw = raw[:truncateAt]
	}
	return path, os.WriteFile(path, raw, 0o600)
}

// ReadCapture is the inverse framing parser. A truncated final frame returns
// the packets decoded so far plus the byte offset where parsing broke.
func ReadCapture(path string) ([]*rtp.Packet, int64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	var pkts []*rtp.Packet
	pos := 0
	for pos+2 <= len(raw) {
		n := int(raw[pos])<<8 | int(raw[pos+1])
		frameStart := pos + 2
		if frameStart+n > len(raw) {
			return pkts, int64(frameStart), errTruncated
		}
		p := &rtp.Packet{}
		if err := p.Unmarshal(raw[frameStart : frameStart+n]); err != nil {
			return pkts, int64(frameStart), err
		}
		pkts = append(pkts, p)
		pos = frameStart + n
	}
	return pkts, -1, nil
}
