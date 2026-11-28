// Package repair locates damage segments and keeps a verifiable map back to the
// original byte offsets. A segment is a maximal run of consecutive invalid
// frames inside one (track, source) boundary; a later valid frame always closes
// the run and is never absorbed into the damaged region.
package repair

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"

	"medcheck/internal/model"
)

// FrameRef pins one damaged frame to its original input position.
type FrameRef struct {
	Seq    uint32
	Micros int64
	Offset model.RawOffset
	Kind   model.ErrorKind
}

// Segment is one contiguous damage region with a deterministic content hash.
type Segment struct {
	Track       uint32
	Source      uint32
	StartMicros int64
	EndMicros   int64
	// ByteSpan is the union [StartOffset, EndOffset) of the damaged records.
	StartOffset int64
	EndOffset   int64
	FirstPos    int64
	Frames      []FrameRef
	SHA256      string
}

// OffsetMap records where every input frame lives, so a recompute can prove no
// repair moved an original byte position. The key includes the input stream
// because sequence numbers restart independently in each stream.
type OffsetMap struct {
	byKey map[offsetKey]model.RawOffset
}

type offsetKey struct {
	stream        string
	track, source uint32
	seq           uint32
}

// NewOffsetMap builds a trace from frames keyed by (stream, track, source, seq).
func NewOffsetMap(frames []*model.Frame) *OffsetMap {
	m := &OffsetMap{byKey: map[offsetKey]model.RawOffset{}}
	for _, f := range frames {
		m.byKey[offsetKey{f.Offset.Stream, f.Track, f.Source, f.Seq}] = f.Offset
	}
	return m
}

// Lookup returns the original offset of a frame.
func (m *OffsetMap) Lookup(stream string, track, source, seq uint32) (model.RawOffset, bool) {
	off, ok := m.byKey[offsetKey{stream, track, source, seq}]
	return off, ok
}

// Len is the number of traced frames.
func (m *OffsetMap) Len() int { return len(m.byKey) }

// isDamaged reports whether a frame belongs to a damage run.
func isDamaged(f *model.Frame) bool { return !f.Valid || f.Err != nil }

// Build groups damaged frames into segments in deterministic frame order. The
// input must already be in the timeline's total order so results are
// independent of arrival order.
func Build(ordered []*model.Frame) []*Segment {
	var segments []*Segment
	var cur *Segment
	flush := func() {
		if cur != nil {
			// Deterministic member order makes the hash independent of the
			// arrival order of frames inside one damage run.
			sort.SliceStable(cur.Frames, func(i, j int) bool {
				if cur.Frames[i].Micros != cur.Frames[j].Micros {
					return cur.Frames[i].Micros < cur.Frames[j].Micros
				}
				if cur.Frames[i].Seq != cur.Frames[j].Seq {
					return cur.Frames[i].Seq < cur.Frames[j].Seq
				}
				return cur.Frames[i].Offset.Start < cur.Frames[j].Offset.Start
			})
			// Re-derive every boundary field from the ordered members so the
			// segment (and its hash) is a pure function of the frame set.
			first, last := cur.Frames[0], cur.Frames[len(cur.Frames)-1]
			cur.StartMicros, cur.EndMicros = first.Micros, last.Micros
			cur.StartOffset, cur.EndOffset = first.Offset.Start, last.Offset.End
			cur.FirstPos = first.Offset.Start
			for _, fr := range cur.Frames {
				if fr.Offset.Start < cur.StartOffset {
					cur.StartOffset = fr.Offset.Start
				}
				if fr.Offset.End > cur.EndOffset {
					cur.EndOffset = fr.Offset.End
				}
			}
			cur.SHA256 = hashSegment(cur)
			segments = append(segments, cur)
			cur = nil
		}
	}
	for _, f := range ordered {
		if !isDamaged(f) {
			flush() // a valid frame terminates any open run
			continue
		}
		micros := f.EventTime.MicrosOr(f.EventTime.Ticks)
		if cur == nil || cur.Track != f.Track || cur.Source != f.Source {
			flush() // source/track switch starts a fresh, separate segment
			cur = &Segment{
				Track:       f.Track,
				Source:      f.Source,
				StartMicros: micros,
				EndMicros:   micros,
				StartOffset: f.Offset.Start,
				EndOffset:   f.Offset.End,
			}
			if f.Err != nil {
				cur.FirstPos = f.Err.FirstPos
			} else {
				cur.FirstPos = f.Offset.Start
			}
		}
		kind := model.KindChecksum
		if f.Err != nil {
			kind = f.Err.Kind
		}
		cur.Frames = append(cur.Frames, FrameRef{
			Seq:    f.Seq,
			Micros: micros,
			Offset: f.Offset,
			Kind:   kind,
		})
		if micros > cur.EndMicros {
			cur.EndMicros = micros
		}
		if f.Offset.Start < cur.StartOffset {
			cur.StartOffset = f.Offset.Start
		}
		if f.Offset.End > cur.EndOffset {
			cur.EndOffset = f.Offset.End
		}
	}
	flush()
	return segments
}

// hashSegment produces an order-stable SHA-256 over the segment's fixed track,
// time boundary and original offsets. Arrival order cannot change the bytes.
func hashSegment(s *Segment) string {
	h := sha256.New()
	writeInt64 := func(v int64) {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(v))
		h.Write(b[:])
	}
	writeUint32 := func(v uint32) {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], v)
		h.Write(b[:])
	}
	writeUint32(s.Track)
	writeUint32(s.Source)
	writeInt64(s.StartMicros)
	writeInt64(s.EndMicros)
	writeInt64(s.StartOffset)
	writeInt64(s.EndOffset)
	writeInt64(s.FirstPos)
	for _, fr := range s.Frames {
		writeUint32(fr.Seq)
		writeInt64(fr.Micros)
		writeInt64(fr.Offset.Start)
		writeInt64(fr.Offset.End)
		writeUint32(uint32(fr.Kind))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// VerifyOffsets confirms every damaged frame still resolves to exactly the
// original offset recorded in trace. It returns the first mismatch, if any.
func VerifyOffsets(segs []*Segment, trace *OffsetMap) (FrameRef, model.RawOffset, bool) {
	for _, s := range segs {
		for _, fr := range s.Frames {
			want, ok := trace.Lookup(fr.Offset.Stream, s.Track, s.Source, fr.Seq)
			if !ok || want != fr.Offset {
				return fr, want, false
			}
		}
	}
	return FrameRef{}, model.RawOffset{}, true
}
