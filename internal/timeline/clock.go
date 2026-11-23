// Package timeline turns per-track RTP-style timestamp streams into a single
// integer-rational master timeline. It expands 32-bit wraparound, labels
// duplicates and regressions, and estimates clock drift from RTP anchors.
package timeline

import (
	"encoding/binary"
	"fmt"

	"github.com/pion/rtp"
	"mediapack/internal/model"
)

// EventKind classifies each timestamp transition.
type EventKind string

const (
	EvtFirst      EventKind = "first"
	EvtAdvance    EventKind = "advance"
	EvtWrap       EventKind = "wrap"       // legal 32-bit wraparound
	EvtDuplicate  EventKind = "duplicate"  // identical timestamp repeated
	EvtRegression EventKind = "regression" // abnormal backwards jump
)

const rtpModulus int64 = 1 << 32

// rtpHeaderLen is the fixed RTP header size in bytes (RFC 3550 §5.1).
const rtpHeaderLen = 12

// ClockEvent is one sample positioned on both the unwrapped clock and the
// nanosecond master timeline.
type ClockEvent struct {
	SampleIndex int       `json:"sample_index"`
	RawTS       uint32    `json:"raw_ts"`
	UnwrappedTS int64     `json:"unwrapped_ts"`
	Delta       int64     `json:"delta_clock_ticks"`
	Kind        EventKind `json:"kind"`
	MasterNS    int64     `json:"master_ns"`
	PacketOff   int64     `json:"packet_offset"`
}

// ClockTable is one track's raw timestamp stream plus its rate/anchor. The
// stream is a concatenation of RTP packets, parsed with pion/rtp.
type ClockTable struct {
	TrackID   uint32
	ClockRate uint32 // RTP clock rate, ticks per second
	BaseTS    uint32 // declared anchor (from container metadata)
	Packets   []rtp.Packet
	Offsets   []int64 // byte offset of each packet in the sidecar stream
}

// TrackTimeline is the fully expanded timeline for one track.
type TrackTimeline struct {
	TrackID      uint32        `json:"track_id"`
	ClockRate    uint32        `json:"clock_rate"`
	AnchorTS     uint32        `json:"anchor_ts"`
	WrapCount    int           `json:"wrap_count"`
	DupCount     int           `json:"dup_count"`
	Events       []ClockEvent  `json:"events"`
	Drift        DriftEstimate `json:"drift"`
	LastMasterNS int64         `json:"last_master_ns"`
}

// Expand unwraps the 32-bit RTP timestamp stream into a monotonic int64 clock,
// classifies every transition and maps each sample to master nanoseconds.
// Regressions are collected as structured errors but expansion continues, so
// all faults in the stream are reported rather than only the first.
func (ct *ClockTable) Expand(es *model.Errors) (*TrackTimeline, error) {
	if ct.ClockRate == 0 {
		return nil, fmt.Errorf("timeline: track %d has zero clock rate", ct.TrackID)
	}
	if len(ct.Packets) == 0 {
		es.Newf(model.StageTimeline, model.CodeMissingTrack, "", ct.TrackID,
			model.ByteRange{}, "track has no timestamp packets")
		return nil, es.Err()
	}
	tl := &TrackTimeline{
		TrackID: ct.TrackID, ClockRate: ct.ClockRate,
		AnchorTS: ct.BaseTS, Events: make([]ClockEvent, 0, len(ct.Packets)),
	}
	var unwrapped int64
	var prevRaw uint32
	for i, pkt := range ct.Packets {
		raw := pkt.Timestamp
		off := int64(0)
		if i < len(ct.Offsets) {
			off = ct.Offsets[i]
		}
		ev := ClockEvent{SampleIndex: i, RawTS: raw, PacketOff: off}
		if i == 0 {
			unwrapped = int64(raw)
			ev.Kind, ev.Delta = EvtFirst, 0
		} else {
			d := modularDelta(raw, prevRaw)
			ev.Delta = d
			switch {
			case d > 0:
				unwrapped += d
				if raw < prevRaw {
					ev.Kind = EvtWrap
					tl.WrapCount++
				} else {
					ev.Kind = EvtAdvance
				}
			case d == 0:
				ev.Kind = EvtDuplicate
				tl.DupCount++
			default:
				ev.Kind = EvtRegression
				es.Newf(model.StageTimeline, model.CodeTimestampBack, "", ct.TrackID,
					model.ByteRange{Offset: off, Length: int64(rtpHeaderLen)},
					"timestamp regression sample=%d raw=%d prev=%d delta=%d",
					i, raw, prevRaw, d)
				unwrapped += d // keep going so later faults are still seen
			}
		}
		ev.UnwrappedTS = unwrapped
		// master ns = (unwrapped - first) / clockRate * 1e9, exact rational.
		delta := unwrapped - int64(ct.Packets[0].Timestamp)
		rel, err := model.NewRat(delta, int64(ct.ClockRate))
		if err != nil {
			return nil, err
		}
		ns, err := rel.Mul(model.MustRat(1_000_000_000, 1))
		if err != nil {
			return nil, err
		}
		ev.MasterNS = ns.Round()
		tl.Events = append(tl.Events, ev)
		prevRaw = raw
	}
	tl.LastMasterNS = tl.Events[len(tl.Events)-1].MasterNS
	return tl, nil
}

// modularDelta returns the signed 32-bit modular difference cur-prev, so a
// legal wraparound appears as a small positive jump rather than a regression.
func modularDelta(cur, prev uint32) int64 {
	// Work entirely in uint32 so the subtraction wraps modulo 2^32, then
	// reinterpret the unsigned 32-bit residue as a signed one.
	u := cur - prev
	if u < 1<<31 {
		return int64(u)
	}
	return int64(u) - rtpModulus
}

// EncodeRTPStream frames RTP packets as a self-describing sidecar: each packet
// is prefixed by a big-endian uint32 on-wire length. Length-prefix framing is
// used because raw concatenated RTP has no in-band boundary marker.
func EncodeRTPStream(packets []rtp.Packet) ([]byte, error) {
	var out []byte
	for i := range packets {
		raw, err := packets[i].Marshal()
		if err != nil {
			return nil, fmt.Errorf("packet %d: %w", i, err)
		}
		hdr := make([]byte, 4)
		binary.BigEndian.PutUint32(hdr, uint32(len(raw)))
		out = append(out, hdr...)
		out = append(out, raw...)
	}
	return out, nil
}

// ParseRTPPacketStream decodes a length-prefixed RTP sidecar stream,
// recording each packet's absolute byte offset (pointing at its length prefix).
func ParseRTPPacketStream(b []byte) ([]rtp.Packet, []int64, error) {
	var pkts []rtp.Packet
	var offs []int64
	off := 0
	for off < len(b) {
		if len(b)-off < 4 {
			return nil, nil, fmt.Errorf("truncated length prefix at offset %d", off)
		}
		n := int(binary.BigEndian.Uint32(b[off:]))
		if n <= 0 || n > (1<<24) || off+4+n > len(b) {
			return nil, nil, fmt.Errorf("illegal RTP packet length %d at offset %d", n, off)
		}
		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(b[off+4 : off+4+n]); err != nil {
			return nil, nil, fmt.Errorf("offset %d: %w", off, err)
		}
		pkts = append(pkts, *pkt)
		offs = append(offs, int64(off))
		off += 4 + n
	}
	return pkts, offs, nil
}
