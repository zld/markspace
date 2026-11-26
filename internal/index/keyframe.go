// Package index turns stamped packet records into keyframe entries and
// persists them in ISO BMFF uuid sidecars. Keyframe detection covers H.264
// (AVC) and HEVC only; any other codec yields explicitly incomplete entries.
package index

import (
	"tsindex/internal/ingest"
)

// Codec identifiers kept stable across the sidecar format.
const (
	CodecH264 = "h264"
	CodecHEVC = "hevc"
)

// Keyframe is one indexed random-access point.
type Keyframe struct {
	Program  uint16
	TrackPID uint16
	Codec    string
	// Ticks is the unified 27 MHz timestamp; SourceOffset is the byte offset
	// of the TS packet carrying the keyframe's first NAL unit.
	Ticks        int64
	SourceOffset int64
	// Complete is false when the codec cannot be keyframe-scanned, in which
	// case the entry is a placeholder marking an incomplete index.
	Complete bool
}

// Scanner tracks PES assembly per PID and recognizes access-point NAL units.
// It is single-goroutine; the pool gives each segment its own scanner.
type Scanner struct {
	programs map[uint16]uint16 // elementary PID -> program number
	codecs   map[uint16]ingest.StreamInfo
	pes      map[uint16][]byte // partial PES payload per PID
	frames   []Keyframe
}

// NewScanner builds a scanner from a validated mapping epoch.
func NewScanner(epoch *ingest.MappingEpoch) *Scanner {
	s := &Scanner{
		programs: map[uint16]uint16{},
		codecs:   map[uint16]ingest.StreamInfo{},
		pes:      map[uint16][]byte{},
	}
	if epoch != nil {
		for prog, pm := range epoch.Programs {
			for pid, info := range pm.Streams {
				s.programs[pid] = prog
				s.codecs[pid] = info
			}
		}
	}
	return s
}

// Feed consumes one stamped record. A keyframe is reported exactly once, at
// the packet whose payload begins (or continues into) an access-point NAL.
func (s *Scanner) Feed(rec *ingest.Record) *Keyframe {
	info, known := s.codecs[rec.PID]
	if !known || !rec.HasPayload {
		return nil
	}
	payload := rec.Payload()
	if rec.PUSI {
		// New PES unit: drop any unfinished previous one, strip the PES
		// header so only the elementary stream remains.
		s.pes[rec.PID] = stripPESHeader(payload)
	} else {
		// Continuation packets extend the current access unit but can never
		// start a keyframe, so they are retained without re-evaluating.
		s.pes[rec.PID] = append(s.pes[rec.PID], payload...)
		return nil
	}
	if !info.Complete {
		// Unsupported codec: emit one incomplete marker at the unit start.
		kf := Keyframe{Program: s.programs[rec.PID], TrackPID: rec.PID,
			Codec: info.Codec, Ticks: rec.Ticks, SourceOffset: rec.Offset,
			Complete: false}
		s.frames = append(s.frames, kf)
		return &s.frames[len(s.frames)-1]
	}
	if isKeyframeNAL(info.Codec, s.pes[rec.PID]) {
		kf := Keyframe{Program: s.programs[rec.PID], TrackPID: rec.PID,
			Codec: info.Codec, Ticks: rec.Ticks, SourceOffset: rec.Offset,
			Complete: true}
		s.frames = append(s.frames, kf)
		return &s.frames[len(s.frames)-1]
	}
	return nil
}

// Frames returns every keyframe detected so far, in stream order.
func (s *Scanner) Frames() []Keyframe {
	out := make([]Keyframe, len(s.frames))
	copy(out, s.frames)
	return out
}

// stripPESHeader removes the PES packet header that precedes the elementary
// stream bytes on a payload-unit-start packet.
func stripPESHeader(payload []byte) []byte {
	if len(payload) < 9 || payload[0] != 0x00 || payload[1] != 0x00 || payload[2] != 0x01 {
		return payload // not a PES start; leave untouched
	}
	hdrLen := 9 + int(payload[8])
	if hdrLen > len(payload) {
		return nil
	}
	return payload[hdrLen:]
}

// isKeyframeNAL reports whether the elementary bytes begin with an
// access-point NAL: an H.264 IDR slice (type 5) / SPS (7), or an HEVC IDR
// (types 19/20) / VPS (32) / SPS (33). Detection scans the Annex-B start
// code prefix and reads the NAL header without allocating a full parser.
func isKeyframeNAL(codec string, es []byte) bool {
	nal := firstNALUnit(es)
	if nal == nil {
		return false
	}
	switch codec {
	case CodecH264:
		// nal_ref_idon/type live in the first header byte; type is low 5 bits.
		t := nal[0] & 0x1F
		return t == 5 || t == 7
	case CodecHEVC:
		if len(nal) < 2 {
			return false
		}
		// HEVC NAL unit type occupies the high 6 bits of the first byte.
		t := nal[0] >> 1
		return t == 19 || t == 20 || t == 32 || t == 33
	default:
		return false
	}
}

// firstNALUnit skips any leading zero bytes and the Annex-B start code
// (00 00 01 or 00 00 00 01) and returns the NAL header bytes.
func firstNALUnit(es []byte) []byte {
	i := 0
	for i < len(es) && es[i] == 0 {
		i++
	}
	if i >= len(es) || es[i] != 0x01 {
		return nil
	}
	i++ // consume the 01 of the start code
	if i >= len(es) {
		return nil
	}
	return es[i:]
}
