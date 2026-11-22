package mp4

import (
	"crypto/sha256"
	"fmt"
	"sort"

	"mediapipe/internal/meta"
)

// Keyframe is one sync sample with its media time and storage digest.
type Keyframe struct {
	TrackID uint32
	Segment uint32
	Index   uint32
	DTS     uint64
	CTS     int64
	Digest  [32]byte
	Size    uint32
}

// Index is the keyframe and fragment index built from a parsed File.
type Index struct {
	Timescale uint32
	Segments  []SegmentEntry
	Keyframes []Keyframe
}

// SegmentEntry records the identity and content summary of one fragment.
type SegmentEntry struct {
	Sequence     uint32
	SampleCount  int
	TrackIDs     []uint32
	StartDTS     map[uint32]uint64
	EndDTS       map[uint32]uint64
	Digest       [32]byte
	KeyframeSeqs []uint32
}

// HashHex returns the segment digest as lowercase hex.
func (s SegmentEntry) HashHex() string { return fmt.Sprintf("%x", s.Digest[:]) }

// BuildIndex derives the keyframe index and per-segment content digests. The
// digest covers fragment sequence, track ownership, sample timing and every
// sample payload digest, so any content or ordering change alters it.
func BuildIndex(f *File) (*Index, error) {
	if f == nil || len(f.Segments) == 0 {
		return nil, fmt.Errorf("%w: empty file", ErrUnsupportedLayout)
	}
	idx := &Index{Timescale: f.Timescale}
	for _, seg := range f.Segments {
		entry := SegmentEntry{Sequence: seg.Sequence, StartDTS: map[uint32]uint64{}, EndDTS: map[uint32]uint64{}}
		h := sha256.New()
		var buf []byte
		buf = append(buf, 'S')
		buf = append(buf, byte(seg.Sequence>>24), byte(seg.Sequence>>16), byte(seg.Sequence>>8), byte(seg.Sequence))
		for _, st := range seg.Tracks {
			entry.TrackIDs = append(entry.TrackIDs, st.TrackID)
			sort.Slice(entry.TrackIDs, func(i, j int) bool { return entry.TrackIDs[i] < entry.TrackIDs[j] })
			entry.StartDTS[st.TrackID] = st.BaseDecode
			var lastEnd uint64
			for i := range st.Samples {
				s := st.Samples[i]
				entry.SampleCount++
				buf = append(buf, 'T', byte(st.TrackID>>24), byte(st.TrackID>>16), byte(st.TrackID>>8), byte(st.TrackID))
				buf = append(buf, byte(s.Index>>24), byte(s.Index>>16), byte(s.Index>>8), byte(s.Index))
				buf = appendU64(buf, s.DTS)
				buf = appendI64(buf, s.CTS)
				buf = appendU32(buf, s.Duration)
				buf = appendU32(buf, s.Size)
				buf = appendU32(buf, s.Flags)
				buf = append(buf, s.Digest[:]...)
				lastEnd = s.DTS + uint64(s.Duration)
				if s.Sync {
					entry.KeyframeSeqs = append(entry.KeyframeSeqs, s.Index)
					idx.Keyframes = append(idx.Keyframes, Keyframe{
						TrackID: st.TrackID, Segment: seg.Sequence, Index: s.Index,
						DTS: s.DTS, CTS: s.CTS, Digest: s.Digest, Size: s.Size,
					})
				}
			}
			entry.EndDTS[st.TrackID] = lastEnd
		}
		for _, e := range seg.Emsg {
			buf = append(buf, 'E')
			buf = appendU32(buf, e.Timescale)
			buf = appendU64(buf, e.Presentation)
			buf = appendU32(buf, e.ID)
			payload, err := meta.EncodeEmsg(e)
			if err != nil {
				return nil, err
			}
			sum := sha256.Sum256(payload)
			buf = append(buf, sum[:]...)
		}
		h.Write(buf)
		copy(entry.Digest[:], h.Sum(nil))
		idx.Segments = append(idx.Segments, entry)
	}
	sort.Slice(idx.Keyframes, func(i, j int) bool {
		if idx.Keyframes[i].DTS != idx.Keyframes[j].DTS {
			return idx.Keyframes[i].DTS < idx.Keyframes[j].DTS
		}
		return idx.Keyframes[i].TrackID < idx.Keyframes[j].TrackID
	})
	return idx, nil
}

func metaEncode(e interface{}) ([]byte, error) { return nil, nil }

func appendU64(b []byte, v uint64) []byte {
	return append(b, byte(v>>56), byte(v>>48), byte(v>>40), byte(v>>32), byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

func appendI64(b []byte, v int64) []byte { return appendU64(b, uint64(v)) }

func appendU32(b []byte, v uint32) []byte {
	return append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

// LookupKeyframe returns the latest keyframe at or before track tick t.
func (idx *Index) LookupKeyframe(trackID uint32, t uint64) (Keyframe, bool) {
	var best Keyframe
	found := false
	for _, k := range idx.Keyframes {
		if k.TrackID != trackID || k.DTS > t {
			continue
		}
		if !found || k.DTS > best.DTS || (k.DTS == best.DTS && k.Segment > best.Segment) {
			best = k
			found = true
		}
	}
	return best, found
}

// Keys returns every keyframe as a stable map for verification.
func (idx *Index) Keys() []Keyframe { return append([]Keyframe(nil), idx.Keyframes...) }

// SegmentHashes returns the hex digest of each segment in sequence order.
func (idx *Index) SegmentHashes() []string {
	out := make([]string, len(idx.Segments))
	for i, s := range idx.Segments {
		out[i] = s.HashHex()
	}
	return out
}
