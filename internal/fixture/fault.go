package fixture

import (
	"encoding/binary"
	"errors"
	"time"

	"streamcheck/internal/ts"
)

// millisDur converts whole milliseconds to a time.Duration.
func millisDur(ms int64) time.Duration { return time.Duration(ms) * time.Millisecond }

// InjectPacketDrop removes n packets from the given segment of a built
// stream, returning a new byte slice so the original stays reusable. The
// removed range lies inside the segment so its boundary alignment survives.
func InjectPacketDrop(s BuiltStream, segment, n int) ([]byte, error) {
	if err := checkSegment(s, segment); err != nil {
		return nil, err
	}
	r := s.Segments[segment]
	maxPackets := (r.End - r.Start) / ts.PacketSize
	if n <= 0 || n > maxPackets {
		return nil, errors.New("fixture: drop count out of range")
	}
	// Drop packets from just after the keyframe packet so the segment's
	// opening PUSI remains intact.
	dropStart := r.Start + ts.PacketSize
	dropEnd := dropStart + n*ts.PacketSize
	if dropEnd > r.End {
		return nil, errors.New("fixture: drop window exceeds segment")
	}
	out := make([]byte, 0, len(s.Data)-(dropEnd-dropStart))
	out = append(out, s.Data[:dropStart]...)
	out = append(out, s.Data[dropEnd:]...)
	return out, nil
}

// InjectSegmentSwap exchanges the byte ranges of two segments, modelling an
// out-of-order mux where whole segments arrive in the wrong order.
func InjectSegmentSwap(s BuiltStream, a, b int) ([]byte, error) {
	if err := checkSegment(s, a); err != nil {
		return nil, err
	}
	if err := checkSegment(s, b); err != nil {
		return nil, err
	}
	if a == b {
		return append([]byte(nil), s.Data...), nil
	}
	lo, hi := a, b
	if lo > hi {
		lo, hi = hi, lo
	}
	rl, rh := s.Segments[lo], s.Segments[hi]
	blockA := append([]byte(nil), s.Data[rl.Start:rl.End]...)
	blockB := append([]byte(nil), s.Data[rh.Start:rh.End]...)
	if len(blockA) != len(blockB) {
		return nil, errors.New("fixture: swap requires equal-size segments")
	}
	out := append([]byte(nil), s.Data...)
	copy(out[rl.Start:rl.End], blockB)
	copy(out[rh.Start:rh.End], blockA)
	return out, nil
}

// InjectCorruptTable flips bytes inside the PSI preamble so the PAT or PMT
// section fails its CRC check while framing stays valid.
func InjectCorruptTable(s BuiltStream, tablePID uint16) ([]byte, error) {
	out := append([]byte(nil), s.Data...)
	idx := findSectionStart(out, tablePID)
	if idx < 0 {
		return nil, errors.New("fixture: requested table PID not found")
	}
	// Corrupt a byte inside the section body, away from the sync header.
	pos := idx + 8
	if pos+4 >= len(out) {
		return nil, errors.New("fixture: section too short to corrupt")
	}
	out[pos] ^= 0xff
	return out, nil
}

// InjectPTSWrap rebuilds the stream with a PTS base placed close to the 33-bit
// modulus so the generated timestamps cross the wrap boundary.
func InjectPTSWrap(tier Tier, numSegs int, segMS int64) (BuiltStream, error) {
	// Leave room for roughly two segment durations before the modulus.
	base := tsModTicks() - 2*uint64(segMS)*90
	if base > tsModTicks() {
		return BuiltStream{}, errors.New("fixture: invalid wrap base")
	}
	return BuildTier(tier, numSegs, millisDur(segMS), Options{PTSBase: base}), nil
}

// findSectionStart locates the first PUSI packet on pid and returns the
// absolute byte offset of the section (after the pointer field).
func findSectionStart(data []byte, pid uint16) int {
	for off := 0; off+ts.PacketSize <= len(data); off += ts.PacketSize {
		pkt := data[off : off+ts.PacketSize]
		p, err := ts.ParsePacket(pkt)
		if err != nil {
			continue
		}
		if p.PID == pid && p.PUSI {
			return off + 4 + 1 // skip header and pointer field
		}
	}
	return -1
}

func checkSegment(s BuiltStream, idx int) error {
	if idx < 0 || idx >= len(s.Segments) {
		return errors.New("fixture: segment index out of range")
	}
	return nil
}

// tsModTicks exposes 2^33 without importing the clock package's internals.
func tsModTicks() uint64 { return 1 << 33 }

// crcRegion returns the bytes covered by a section CRC for external checks.
func crcRegion(section []byte) []byte { return section[:len(section)-4] }

// sectionLength reads the section_length field from a raw section.
func sectionLength(section []byte) uint16 {
	if len(section) < 3 {
		return 0
	}
	return binary.BigEndian.Uint16(section[1:3]) & 0x0fff
}
