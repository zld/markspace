// RTP packet handling for one track: 16-bit wire sequence numbers are
// expanded onto a 32-bit extending counter (modulo 2^32), reordered inside a
// bounded jitter window, deduplicated, and assembled into whole frames.
package ingest

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/pion/rtp"

	"medialab/internal/model"
)

// RTP header extension ids carried by every sample packet.
const (
	ExtFrameIndex    uint8 = 1 // manifest frame index this packet belongs to
	ExtFragmentCount uint8 = 2 // total fragments for the frame
	ExtFragmentIndex uint8 = 3 // zero-based fragment position
)

// WireSeqMask is the RTP on-wire sequence space.
const WireSeqMask = 0xffff

// RTPClockRate is the sample clock the fixtures stamp packets with.
const RTPClockRate uint32 = 90000

// Packet is the project's RTP representation of one frame fragment. Seq is the
// 16-bit wire value; ExpandSeqs turns a stream of them into 32-bit counters.
// Frame is the track-local, zero-based frame position, so faults can name the
// frame's 1-based sequence without knowing other tracks' layouts.
type Packet struct {
	Track  model.TrackID
	Seq    uint32
	RTPTs  uint32
	Marker bool
	Frame  int
	Parts  int
	Part   int
	Data   []byte
}

// MarshalPacket serialises a packet with pion/rtp.
func MarshalPacket(p Packet) ([]byte, error) {
	if p.Parts <= 0 || p.Part < 0 || p.Part >= p.Parts {
		return nil, model.NewFault(model.KindIngest, model.CodePayloadCorrupt, p.Track, uint32(p.Frame+1),
			fmt.Sprintf("invalid fragment %d of %d", p.Part, p.Parts))
	}
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    uint8(p.Track),
			SequenceNumber: uint16(p.Seq & WireSeqMask),
			Timestamp:      p.RTPTs,
			Marker:         p.Marker,
			SSRC:           uint32(p.Track),
		},
		Payload: p.Data,
	}
	setExt := func(id uint8, v uint32) {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], v)
		_ = pkt.Header.SetExtension(id, b[:])
	}
	setExt(ExtFrameIndex, uint32(p.Frame))
	setExt(ExtFragmentCount, uint32(p.Parts))
	setExt(ExtFragmentIndex, uint32(p.Part))
	return pkt.Marshal()
}

// UnmarshalPacket parses a wire packet back into the project form.
func UnmarshalPacket(b []byte) (Packet, error) {
	var pkt rtp.Packet
	if err := pkt.Unmarshal(b); err != nil {
		return Packet{}, model.NewFault(model.KindIngest, model.CodePayloadCorrupt, 0, 0,
			fmt.Sprintf("parse RTP packet: %v", err))
	}
	getExt := func(id uint8) (uint32, bool) {
		raw := pkt.Header.GetExtension(id)
		if len(raw) != 4 {
			return 0, false
		}
		return binary.BigEndian.Uint32(raw), true
	}
	frame, okF := getExt(ExtFrameIndex)
	parts, okP := getExt(ExtFragmentCount)
	part, okI := getExt(ExtFragmentIndex)
	if !okF || !okP || !okI {
		return Packet{}, model.NewFault(model.KindIngest, model.CodePayloadCorrupt,
			model.TrackID(pkt.PayloadType), 0, "RTP packet is missing the project header extensions")
	}
	if parts == 0 || part >= parts {
		return Packet{}, model.NewFault(model.KindIngest, model.CodePayloadCorrupt,
			model.TrackID(pkt.PayloadType), 0, fmt.Sprintf("invalid fragment %d of %d", part, parts))
	}
	return Packet{
		Track:  model.TrackID(pkt.PayloadType),
		Seq:    uint32(pkt.SequenceNumber),
		RTPTs:  pkt.Timestamp,
		Marker: pkt.Marker,
		Frame:  int(frame),
		Parts:  int(parts),
		Part:   int(part),
		Data:   append([]byte(nil), pkt.Payload...),
	}, nil
}

// SeqExpansion is the 32-bit extending counter derived from wire sequences.
type SeqExpansion struct {
	Values  []uint32 // parallel to the input, one expanded value per packet
	Wrapped bool     // true when the 32-bit counter crossed its wrap point
}

// ExpandSeqs turns 16-bit wire sequence numbers into a 32-bit extending
// counter modulo 2^32. Each wire value is placed at the extended position
// nearest the running counter, so late and early arrivals both land on their
// true slot and a stream crossing 0xffff is handled without inventing gaps.
// The extended counter itself wraps at 2^32; Wrapped records that it did.
func ExpandSeqs(wire []uint32) (SeqExpansion, error) {
	var out SeqExpansion
	if len(wire) == 0 {
		return out, nil
	}
	for _, w := range wire {
		if w > WireSeqMask {
			return out, model.NewFault(model.KindIngest, model.CodeSeqInvalid, 0, 0,
				fmt.Sprintf("sequence %d exceeds the 16-bit RTP space", w))
		}
	}
	out.Values = make([]uint32, len(wire))
	// running tracks the un-wrapped position so the 2^31 window stays exact
	// even when the counter crosses 2^32.
	running := int64(wire[0])
	for i, w := range wire {
		delta := int64(int32(w) - int32(running&WireSeqMask))
		// Bring the signed 16-bit delta into (-2^15, 2^15]; this is the
		// standard RTP sequence-unwrapping rule.
		if delta > 1<<15 {
			delta -= 1 << 16
		} else if delta <= -1<<15 {
			delta += 1 << 16
		}
		running += delta
		out.Values[i] = uint32(running & 0xffffffff)
		if running < 0 || running > 0xffffffff {
			out.Wrapped = true
		}
	}
	return out, nil
}

// ReorderConfig bounds the jitter window.
type ReorderConfig struct {
	// Window is the maximum number of consecutive missing positions that the
	// jitter buffer is allowed to repair by refilling.
	Window int
	// MaxPackets caps the buffered packets of one track (0 = unlimited).
	MaxPackets int
}

// ReorderResult is the outcome of one track's packet reordering.
type ReorderResult struct {
	Packets    []Packet
	Expanded   []uint32 // parallel to Packets, monotone
	Missing    []uint32 // missing expanded wire positions, ascending
	Duplicates int
	Filled     int
	Warnings   model.Faults
	Fatal      *model.Fault
	// Track is the stream's track id; it is set even when the reorder fails,
	// so callers can attribute a fatal fault without inspecting the packets.
	Track model.TrackID
}

// Reorder deduplicates, reorders and gap-checks one track's packets. Corrupt
// or empty payloads stop the track (Fatal), preserving the offset of the last
// good packet; missing packets are recoverable and counted as fill frames.
func Reorder(packets []Packet, cfg ReorderConfig) ReorderResult {
	var res ReorderResult
	if cfg.Window < 1 {
		cfg.Window = 1
	}
	if len(packets) == 0 {
		return res
	}
	track := packets[0].Track
	res.Track = track
	if cfg.MaxPackets > 0 && len(packets) > cfg.MaxPackets {
		res.Fatal = model.NewFault(model.KindIngest, model.CodeCacheOverflow, track, 0,
			fmt.Sprintf("%d packets exceed the per-track budget of %d", len(packets), cfg.MaxPackets))
		return res
	}

	wire := make([]uint32, len(packets))
	for i, p := range packets {
		wire[i] = p.Seq
	}
	exp, err := ExpandSeqs(wire)
	if err != nil {
		res.Fatal = fault(err)
		res.Fatal = model.WithFault(res.Fatal, track, 0)
		return res
	}
	if exp.Wrapped {
		res.Warnings = append(res.Warnings, model.NewFault(model.KindIngest, model.CodeSeqInvalid,
			track, 0, "32-bit sequence counter wrapped during the stream").RecoverableFault())
	}

	type entry struct {
		pkt Packet
		seq uint32
	}
	entries := make([]entry, len(packets))
	for i, p := range packets {
		if err := validatePayload(p); err != nil {
			res.Fatal = model.WithFault(fault(err), p.Track, uint32(p.Frame+1))
			return res
		}
		entries[i] = entry{p, exp.Values[i]}
	}

	sort.SliceStable(entries, func(a, b int) bool { return entries[a].seq < entries[b].seq })

	deduped := entries[:0]
	for i, e := range entries {
		if i > 0 && e.seq == deduped[len(deduped)-1].seq {
			res.Duplicates++
			continue
		}
		deduped = append(deduped, e)
	}
	// Every fault below names the first frame of the stream (sequence 1).
	firstFrame := uint32(deduped[0].pkt.Frame + 1)
	if res.Duplicates > 0 {
		res.Warnings = append(res.Warnings, model.NewFault(model.KindIngest,
			model.CodePacketDuplicate, track, firstFrame,
			fmt.Sprintf("%d duplicate packet(s) dropped", res.Duplicates)).RecoverableFault())
	}

	first := deduped[0].seq
	last := deduped[len(deduped)-1].seq
	// The extent of the expanded stream, not the running counter, is what
	// bounds an implausible gap; a wrap of the 32-bit counter can legitimately
	// place the last position below the first.
	if span := last - first + 1; span == 0 || span > 1<<20 {
		res.Fatal = model.NewFault(model.KindIngest, model.CodeSeqInvalid, track, firstFrame,
			fmt.Sprintf("reordered stream spans %d positions, beyond the plausible range", span))
		return res
	}
	seen := make(map[uint32]bool, len(deduped))
	for _, e := range deduped {
		if _, dup := seen[e.seq]; dup {
			continue
		}
		seen[e.seq] = true
	}
	for s := first; s <= last; s++ {
		if !seen[s] {
			res.Missing = append(res.Missing, s)
		}
	}
	if len(res.Missing) > 0 {
		res.Filled = len(res.Missing)
		msg := fmt.Sprintf("%d missing packet(s); fill frames will be recorded", len(res.Missing))
		if len(res.Missing) > cfg.Window {
			msg = fmt.Sprintf("%d missing packet(s), beyond the %d-packet window; treated as recoverable",
				len(res.Missing), cfg.Window)
		}
		res.Warnings = append(res.Warnings, model.NewFault(model.KindIngest,
			model.CodeFrameMissing, track, firstFrame, msg).RecoverableFault())
	}

	res.Packets = make([]Packet, len(deduped))
	res.Expanded = make([]uint32, len(deduped))
	for i, e := range deduped {
		res.Packets[i] = e.pkt
		res.Expanded[i] = e.seq
	}
	return res
}

// validatePayload catches empty or ill-formed fragments that cannot be
// reassembled. A corrupt payload stops the track rather than being repaired.
func validatePayload(p Packet) error {
	if len(p.Data) == 0 {
		return model.NewFault(model.KindIngest, model.CodePayloadCorrupt, p.Track, uint32(p.Frame+1),
			"empty fragment payload")
	}
	if p.Parts <= 0 || p.Part < 0 || p.Part >= p.Parts {
		return model.NewFault(model.KindIngest, model.CodePayloadCorrupt, p.Track, uint32(p.Frame+1),
			fmt.Sprintf("invalid fragment %d of %d", p.Part, p.Parts))
	}
	// The marker belongs on the final fragment only; setting it earlier means
	// the sender signalled end-of-frame in the middle of the payload.
	if p.Part < p.Parts-1 && p.Marker {
		return model.NewFault(model.KindIngest, model.CodePayloadCorrupt, p.Track, uint32(p.Frame+1),
			"marker bit set before the final fragment")
	}
	return nil
}

// Assemble joins reordered fragments into complete frame payloads. A frame
// with a missing fragment is omitted (reported as recoverable), never emitted
// partially.
func Assemble(packets []Packet) (map[int][]byte, model.Faults) {
	type frameParts struct {
		track model.TrackID
		parts [][]byte
		seen  []bool
		total int
	}
	frames := make(map[int]*frameParts)
	var order []int
	for _, p := range packets {
		fp, ok := frames[p.Frame]
		if !ok {
			fp = &frameParts{track: p.Track, total: p.Parts,
				seen: make([]bool, p.Parts), parts: make([][]byte, p.Parts)}
			frames[p.Frame] = fp
			order = append(order, p.Frame)
		}
		if fp.total != p.Parts {
			continue
		}
		if !fp.seen[p.Part] {
			fp.seen[p.Part] = true
			fp.parts[p.Part] = p.Data
		}
	}
	sort.Ints(order)
	var warnings model.Faults
	out := make(map[int][]byte, len(frames))
	for _, idx := range order {
		fp := frames[idx]
		complete := true
		var buf bytes.Buffer
		for i := 0; i < fp.total; i++ {
			if !fp.seen[i] {
				complete = false
				warnings = append(warnings, model.NewFault(model.KindIngest,
					model.CodeFrameMissing, fp.track, uint32(idx+1),
					fmt.Sprintf("frame %d: fragment %d of %d missing", idx+1, i, fp.total)).RecoverableFault())
				continue
			}
			buf.Write(fp.parts[i])
		}
		if complete {
			out[idx] = buf.Bytes()
		}
	}
	return out, warnings
}
