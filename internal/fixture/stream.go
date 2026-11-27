package fixture

import (
	"sort"

	"github.com/pion/rtp"
)

// DamageConfig selects which network impairments the generator applies.
type DamageConfig struct {
	// LossUnits are media-unit indices removed from the wire (per track).
	LossUnits map[uint32][]int
	// DuplicateUnits are units sent twice (the copy keeps the same seq).
	DuplicateUnits map[uint32][]int
	// CorruptUnits have their payload checksum invalidated.
	CorruptUnits map[uint32][]int
	// TruncateUnits are payloads cut short (truncation, not full loss).
	TruncateUnits map[uint32][]int
	// ReorderDistance performs one deterministic swap: the packet at index i
	// is moved i+ReorderDistance positions later in the wire order.
	ReorderDistance int
	// WrapStartSeq overrides each track's StartSeq to exercise 16-bit wrap
	// (e.g. 65534 -> 65535 -> 0 -> 1). 0 means "use the track default".
	WrapStartSeq uint16
}

// RTCPReport is a minimal Sender Report carrying the RTP-to-wallclock map.
// Times are stored already converted to the unified rational scale by the
// RTCP package; here we keep raw NTP-ish seconds and RTP tick origin.
type RTCPReport struct {
	TrackID    uint32
	SSRC       uint32
	NTPSeconds uint32 // whole seconds since a fixed epoch (base 0)
	NTPFrac    uint32 // 32-bit fractional second
	RTPTime    uint32 // RTP timestamp at NTP instant
	ClockRate  uint32
	PacketCount uint32
	OctetCount  uint32
}

// Stream is the complete generated fixture with its ground truth.
type Stream struct {
	Tracks  []TrackConfig
	Packets []*RawPacket // wire order after damage injection
	Reports []RTCPReport
	// Ground truth, deterministic for a seed/config pair.
	VideoTrackID uint32
	AudioTrackID uint32
	LostUnits    map[uint32][]int
	CorruptUnits map[uint32][]int
	// RecoverableUnits are lost units whose neighbors survive; these are the
	// denominator the loss-recovery-rate assertion uses.
	RecoverableUnits map[uint32][]int
	// Reordered is true when ReorderDistance actually moved a packet.
	Reordered bool
	// Wrapped is true when at least one track crosses the 16-bit boundary.
	Wrapped bool
}

// Build generates both tracks, applies impairments deterministically and
// produces matching RTCP sender reports.
func Build(rngSeed int64, video, audio TrackConfig, dmg DamageConfig) (*Stream, error) {
	if video.Units <= 0 || audio.Units <= 0 {
		return nil, errBadConfig
	}
	if dmg.WrapStartSeq != 0 {
		video.StartSeq = dmg.WrapStartSeq
		audio.StartSeq = dmg.WrapStartSeq
	}
	s := &Stream{
		Tracks:           []TrackConfig{video, audio},
		LostUnits:        cloneUnits(dmg.LossUnits),
		CorruptUnits:     mergeUnits(dmg.CorruptUnits, dmg.TruncateUnits),
		RecoverableUnits: map[uint32][]int{},
		VideoTrackID:     video.ID,
		AudioTrackID:     audio.ID,
	}
	s.Wrapped = wrapsBoundary(video) || wrapsBoundary(audio)

	videoPkts := generateTrack(video)
	audioPkts := generateTrack(audio)
	all := make([]*RawPacket, 0, len(videoPkts)+len(audioPkts))
	all = append(all, videoPkts...)
	all = append(all, audioPkts...)

	applyCorruption(all, dmg)
	all = applyDuplication(all, dmg)
	all = applyLoss(all, dmg, s)
	all, s.Reordered = applyReorder(all, dmg.ReorderDistance)

	// Interleave tracks deterministically: sort on wire position derived
	// from sequence/track so receivers see a realistic mixed stream, while
	// the reorder swap above stays visible inside each track.
	interleave(all)
	s.Packets = all

	s.Reports = []RTCPReport{
		buildReport(video),
		buildReport(audio),
	}
	return s, nil
}

func wrapsBoundary(c TrackConfig) bool {
	last := int(c.StartSeq) + c.Units - 1
	return last > 0xFFFF
}

func applyCorruption(all []*RawPacket, dmg DamageConfig) {
	corrupt := indexSet(dmg.CorruptUnits)
	trunc := indexSet(dmg.TruncateUnits)
	for _, rp := range all {
		key := pairKey(rp.TrackID, int(rp.UnitIndex))
		if corrupt[key] {
			// Flip a body byte; header magic stays valid but checksum fails.
			p := rp.Packet.Payload
			if len(p) > MediaHeaderLen {
				p[MediaHeaderLen] ^= 0xFF
			}
		}
		if trunc[key] && len(rp.Packet.Payload) > MediaHeaderLen+2 {
			// Keep the full header (magic stays valid) but drop most of the
			// body: the parser then sees a declared BodyLen that the wire
			// bytes cannot satisfy, i.e. a truncated media unit.
			rp.Packet.Payload = rp.Packet.Payload[:MediaHeaderLen+2]
		}
	}
}

func applyDuplication(all []*RawPacket, dmg DamageConfig) []*RawPacket {
	dup := indexSet(dmg.DuplicateUnits)
	out := make([]*RawPacket, 0, len(all)*2)
	for _, rp := range all {
		out = append(out, rp)
		if dup[pairKey(rp.TrackID, int(rp.UnitIndex))] {
			clone := *rp
			clone.Packet = rp.Packet.Clone()
			out = append(out, &clone)
		}
	}
	return out
}

func applyLoss(all []*RawPacket, dmg DamageConfig, s *Stream) []*RawPacket {
	loss := indexSet(dmg.LossUnits)
	out := make([]*RawPacket, 0, len(all))
	// A lost unit is "recoverable" when at least one adjacent unit on the
	// same track survives: the analyzer can bound the gap but never invents
	// the unit itself; recovery here means the gap is parsed and bounded.
	for _, rp := range all {
		if loss[pairKey(rp.TrackID, int(rp.UnitIndex))] {
			continue
		}
		out = append(out, rp)
	}
	for trackID, units := range dmg.LossUnits {
		present := presentIndex(all, trackID)
		var rec []int
		for _, u := range units {
			if present[u-1] || present[u+1] {
				rec = append(rec, u)
			}
		}
		sort.Ints(rec)
		s.RecoverableUnits[trackID] = rec
	}
	return out
}

func applyReorder(all []*RawPacket, dist int) ([]*RawPacket, bool) {
	if dist <= 0 || len(all) <= dist {
		return all, false
	}
	// Move the first packet of the video track `dist` positions later within
	// its own track segment; receivers must reorder it back by extended seq.
	var trackID uint32
	for _, rp := range all {
		trackID = rp.TrackID
		break
	}
	var segment []*RawPacket
	var rest []*RawPacket
	for _, rp := range all {
		if rp.TrackID == trackID {
			segment = append(segment, rp)
		} else {
			rest = append(rest, rp)
		}
	}
	if len(segment) <= dist {
		return all, false
	}
	// segment[0] moves to index dist: [1..dist] shift left by one.
	moved := segment[0]
	copy(segment[0:dist], segment[1:dist+1])
	segment[dist] = moved
	return append(segment, rest...), true
}

// interleave produces a stable mixed-track wire order: video and audio
// packets alternate by tick so a receiver genuinely has to demux SSRCs.
func interleave(all []*RawPacket) {
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.Packet.Timestamp != b.Packet.Timestamp {
			return a.Packet.Timestamp < b.Packet.Timestamp
		}
		if a.TrackID != b.TrackID {
			return a.TrackID < b.TrackID
		}
		return extSeq(a.Packet.SequenceNumber) < extSeq(b.Packet.SequenceNumber)
	})
}

// extSeq gives wrap-free ordering for the initial 16-bit comparison in
// interleave; the real extension algorithm lives in the rtp package.
func extSeq(seq uint16) uint32 { return uint32(seq) }

func buildReport(cfg TrackConfig) RTCPReport {
	return RTCPReport{
		TrackID: cfg.ID, SSRC: cfg.SSRC,
		NTPSeconds:  1,
		NTPFrac:     0,
		RTPTime:     cfg.StartTS,
		ClockRate:   cfg.ClockRate,
		PacketCount: uint32(cfg.Units),
		OctetCount:  uint32(cfg.Units) * (MediaHeaderLen + 24),
	}
}

func indexSet(m map[uint32][]int) map[uint64]bool {
	out := make(map[uint64]bool)
	for id, units := range m {
		for _, u := range units {
			out[pairKey(id, u)] = true
		}
	}
	return out
}

func pairKey(trackID uint32, unit int) uint64 {
	return uint64(trackID)<<32 | uint64(uint32(unit))
}

func presentIndex(all []*RawPacket, trackID uint32) map[int]bool {
	out := make(map[int]bool)
	for _, rp := range all {
		if rp.TrackID == trackID {
			out[int(rp.UnitIndex)] = true
		}
	}
	return out
}

func cloneUnits(m map[uint32][]int) map[uint32][]int {
	out := make(map[uint32][]int, len(m))
	for k, v := range m {
		cp := append([]int(nil), v...)
		sort.Ints(cp)
		out[k] = cp
	}
	return out
}

// mergeUnits unions two per-track unit-index maps (corrupt + truncated are
// both damage classes the locator must report).
func mergeUnits(a, b map[uint32][]int) map[uint32][]int {
	out := map[uint32][]int{}
	for _, m := range []map[uint32][]int{a, b} {
		for k, v := range m {
			out[k] = append(out[k], v...)
		}
	}
	for k := range out {
		sort.Ints(out[k])
	}
	return out
}

// MarshalPackets serializes the wire packets for receiver tests.
func MarshalPackets(pkts []*RawPacket) ([][]byte, error) {
	out := make([][]byte, 0, len(pkts))
	for _, rp := range pkts {
		b, err := rp.Packet.Marshal()
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// WirePackets returns the pion RTP packets in current wire order.
func (s *Stream) WirePackets() []*rtp.Packet {
	out := make([]*rtp.Packet, 0, len(s.Packets))
	for _, rp := range s.Packets {
		out = append(out, rp.Packet)
	}
	return out
}
