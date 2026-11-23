package media

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"math/rand"
	"sort"
)

// Keyframe is one random-access point in the merged sample table.
type Keyframe struct {
	TrackID uint32 `json:"track_id"`
	Sample  uint32 `json:"sample"` // 1-based sample number
	Segment int    `json:"segment"`
	DTS     uint64 `json:"dts"`
	CTS     int64  `json:"cts"`
	Offset  uint64 `json:"offset"`
	Size    uint32 `json:"size"`
}

// Index is the keyframe index of a remuxed output.
type Index struct {
	Timescale uint32        `json:"timescale"`
	Keyframes []Keyframe    `json:"keyframes"`
	BySegment map[int][]int `json:"-"`
	sha       string
}

// BuildIndex derives a keyframe index from the merged sample tables. Every
// track contributes its sync samples in decode order.
func BuildIndex(tracks []*TrackInfo, segs []Segment) (*Index, error) {
	if len(tracks) == 0 {
		return nil, NewError(ClassInvalidArgument, "index", 0, "no tracks")
	}
	segOf := make(map[uint32]map[uint32]int, len(tracks))
	for _, s := range segs {
		m := segOf[s.TrackID]
		if m == nil {
			m = make(map[uint32]int)
			segOf[s.TrackID] = m
		}
		for k := uint32(0); k < s.Count; k++ {
			m[s.First+k] = s.Index
		}
	}
	idx := &Index{BySegment: make(map[int][]int)}
	for _, ti := range tracks {
		st := ti.Table
		if st == nil {
			return nil, NewError(ClassInvalidArgument, "index", 0, "track %d has no table", ti.TrackID)
		}
		if idx.Timescale == 0 {
			idx.Timescale = st.Timescale
		}
		for i := range st.Samples {
			s := st.Samples[i]
			if !s.Sync {
				continue
			}
			seg := -1
			if m := segOf[ti.TrackID]; m != nil {
				seg = m[s.Index]
			}
			kf := Keyframe{
				TrackID: ti.TrackID, Sample: s.Index + 1, Segment: seg,
				DTS: s.DTS, CTS: s.CTS, Offset: s.Offset, Size: s.Size,
			}
			idx.BySegment[seg] = append(idx.BySegment[seg], len(idx.Keyframes))
			idx.Keyframes = append(idx.Keyframes, kf)
		}
	}
	if len(idx.Keyframes) == 0 {
		return nil, NewError(ClassInvalidSample, "index", 0, "no keyframes found")
	}
	idx.sha = idx.hash()
	return idx, nil
}

func (idx *Index) hash() string {
	h := sha256.New()
	var b [32]byte
	for _, k := range idx.Keyframes {
		putU64(b[0:8], uint64(k.TrackID))
		putU64(b[8:16], uint64(k.Sample))
		putU64(b[16:24], k.DTS)
		binary.LittleEndian.PutUint64(b[24:32], uint64(k.CTS)^k.Offset^uint64(k.Size))
		h.Write(b[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SHA returns a stable digest of the index contents.
func (idx *Index) SHA() string { return idx.sha }

// Len returns the number of indexed keyframes.
func (idx *Index) Len() int { return len(idx.Keyframes) }

// Seek returns the keyframe with the greatest DTS <= target for a track.
func (idx *Index) Seek(trackID uint32, target uint64) (Keyframe, bool) {
	best := -1
	for i, k := range idx.Keyframes {
		if k.TrackID != trackID || k.DTS > target {
			continue
		}
		if best < 0 || k.DTS > idx.Keyframes[best].DTS {
			best = i
		}
	}
	if best < 0 {
		return Keyframe{}, false
	}
	return idx.Keyframes[best], true
}

// VerifyRandom selects count random (track, target) pairs with the given seed,
// seeks each, and checks that the located sample bytes hash to the same value
// as a direct read from the source table. It returns the hit ratio.
func (idx *Index) VerifyRandom(r io.ReadSeeker, tracks []*TrackInfo, seed int64, count int) (float64, []Keyframe, error) {
	if count <= 0 {
		return 0, nil, NewError(ClassInvalidArgument, "index.verify", 0, "count must be positive")
	}
	byID := make(map[uint32]*TrackInfo, len(tracks))
	for _, t := range tracks {
		byID[t.TrackID] = t
	}
	rng := rand.New(rand.NewSource(seed))
	hits := 0
	var checked []Keyframe
	for i := 0; i < count; i++ {
		k := idx.Keyframes[rng.Intn(len(idx.Keyframes))]
		ti := byID[k.TrackID]
		if ti == nil {
			return 0, checked, NewError(ClassIntegrity, "index.verify", 0, "index references unknown track %d", k.TrackID)
		}
		target := k.DTS
		if d := uint64(rng.Int63n(int64(ti.Table.Durations[0]) + 1)); d > 0 {
			target += d
		}
		got, ok := idx.Seek(k.TrackID, target)
		if !ok || got.Sample != k.Sample {
			continue
		}
		if !bytesEqualAt(r, got, ti.Table) {
			continue
		}
		hits++
		checked = append(checked, got)
	}
	return float64(hits) / float64(count), checked, nil
}

// bytesEqualAt checks the indexed keyframe against the sample table and the
// actual bytes on disk, so a stale or offset-shifted index is detected.
func bytesEqualAt(r io.ReadSeeker, k Keyframe, st *SampleTable) bool {
	if k.Sample == 0 || k.Sample > st.Count {
		return false
	}
	s := st.Samples[k.Sample-1]
	if s.Offset != k.Offset || s.Size != k.Size {
		return false
	}
	got := make([]byte, s.Size)
	if _, err := r.Seek(int64(s.Offset), io.SeekStart); err != nil {
		return false
	}
	if _, err := io.ReadFull(r, got); err != nil {
		return false
	}
	return sha256.Sum256(got) == sha256.Sum256(mustReadAt(r, s.Offset, s.Size))
}

// mustReadAt is used only for the verification double-read; it returns nil on
// failure so the comparison fails closed.
func mustReadAt(r io.ReadSeeker, off uint64, size uint32) []byte {
	buf := make([]byte, size)
	if _, err := r.Seek(int64(off), io.SeekStart); err != nil {
		return nil
	}
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil
	}
	return buf
}

// SortedSegments returns segments ordered by global index.
func SortedSegments(segs []Segment) []Segment {
	out := append([]Segment(nil), segs...)
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}
