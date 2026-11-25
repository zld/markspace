// Package index builds the keyframe index for validated container tracks
// and segments. An index is only ever produced from objects that passed
// container validation; unconfirmable keyframes are marked Unknown rather
// than guessed.
package index

import (
	"sort"
	"sync"

	"mediascan/internal/errs"
)

// KeyframeState classifies a keyframe entry's confidence.
type KeyframeState string

const (
	// StateKnown is a keyframe confirmed by an stss entry or explicit marker.
	StateKnown KeyframeState = "known"
	// StateUnknown means the source could not confirm a keyframe here;
	// the entry is retained for reconstruction but never guessed as sync.
	StateUnknown KeyframeState = "unknown"
)

// Keyframe is one indexed sync-sample position.
type Keyframe struct {
	TrackID      uint32
	SampleNumber uint32 // 1-based sample number within the track
	SegmentIndex int64
	TimeTicks    int64 // track-timescale ticks
	ByteOffset   int64 // absolute file offset of the sample
	State        KeyframeState
}

// Index is the per-run keyframe index. It is safe for concurrent chunk
// builders because batch processing fans out across chunks.
type Index struct {
	mu        sync.RWMutex
	Keyframes []Keyframe
	// rejected tracks object IDs whose errors keep them out of the index.
	rejected map[string]bool
}

// New creates an empty index.
func New() *Index {
	return &Index{rejected: map[string]bool{}}
}

// Add appends a confirmed keyframe. Entries are kept sorted for stable
// hashing and deterministic output across repeated runs.
func (idx *Index) Add(k Keyframe) error {
	if k.TrackID == 0 {
		return errs.New(errs.KindIndex, "", errs.Unknown, k.SegmentIndex,
			k.TrackID, "keyframe requires a non-zero track id")
	}
	if k.SampleNumber == 0 {
		return errs.New(errs.KindIndex, "", k.ByteOffset, k.SegmentIndex,
			k.TrackID, "keyframe sample numbers are 1-based")
	}
	if k.State == "" {
		k.State = StateKnown
	}
	idx.mu.Lock()
	idx.Keyframes = append(idx.Keyframes, k)
	idx.mu.Unlock()
	return nil
}

// Reject records an object that failed validation: it must never appear
// in the index. Re-checking an already rejected object is a no-op.
func (idx *Index) Reject(objectID string) {
	idx.mu.Lock()
	idx.rejected[objectID] = true
	idx.mu.Unlock()
}

// IsRejected reports whether an object was barred from indexing.
func (idx *Index) IsRejected(objectID string) bool {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.rejected[objectID]
}

// Finalize sorts entries and verifies internal consistency. It is called
// once after every chunk has been processed.
func (idx *Index) Finalize() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	sort.SliceStable(idx.Keyframes, func(i, j int) bool {
		if idx.Keyframes[i].TrackID != idx.Keyframes[j].TrackID {
			return idx.Keyframes[i].TrackID < idx.Keyframes[j].TrackID
		}
		if idx.Keyframes[i].SegmentIndex != idx.Keyframes[j].SegmentIndex {
			return idx.Keyframes[i].SegmentIndex < idx.Keyframes[j].SegmentIndex
		}
		return idx.Keyframes[i].SampleNumber < idx.Keyframes[j].SampleNumber
	})
	seen := map[[3]uint64]bool{}
	for _, k := range idx.Keyframes {
		key := [3]uint64{uint64(k.TrackID), uint64(k.SegmentIndex), uint64(k.SampleNumber)}
		if seen[key] {
			return errs.New(errs.KindIndex, "", k.ByteOffset, k.SegmentIndex,
				k.TrackID, "duplicate keyframe (track=%d seg=%d sample=%d)",
				k.TrackID, k.SegmentIndex, k.SampleNumber)
		}
		seen[key] = true
	}
	return nil
}

// HitRate returns the fraction of requested (track, segment) boundaries
// that have at least one known keyframe. Unknown-only segments count as
// misses, reflecting that no sync point was confirmable there.
func (idx *Index) HitRate(wanted []Boundary) float64 {
	if len(wanted) == 0 {
		return 0
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	hits := 0
	for _, w := range wanted {
		if idx.hasKnown(w.TrackID, w.SegmentIndex) {
			hits++
		}
	}
	return float64(hits) / float64(len(wanted))
}

// Boundary identifies one (track, segment) that should carry a keyframe.
type Boundary struct {
	TrackID      uint32
	SegmentIndex int64
}

func (idx *Index) hasKnown(trackID uint32, segment int64) bool {
	for _, k := range idx.Keyframes {
		if k.TrackID == trackID && k.SegmentIndex == segment && k.State == StateKnown {
			return true
		}
	}
	return false
}

// KnownCount / UnknownCount summarize index confidence.
func (idx *Index) KnownCount() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	n := 0
	for _, k := range idx.Keyframes {
		if k.State == StateKnown {
			n++
		}
	}
	return n
}

// UnknownCount reports entries that could not be confirmed as sync points.
func (idx *Index) UnknownCount() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	n := 0
	for _, k := range idx.Keyframes {
		if k.State == StateUnknown {
			n++
		}
	}
	return n
}

// Len reports total indexed entries.
func (idx *Index) Len() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.Keyframes)
}

// Snapshot returns a copy of the keyframes for serialization.
func (idx *Index) Snapshot() []Keyframe {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	out := make([]Keyframe, len(idx.Keyframes))
	copy(out, idx.Keyframes)
	return out
}

// Rebuild reconstructs an index from confirmed keyframe records. Any
// record marked unknown stays unknown; Rebuild never promotes it.
func Rebuild(records []Keyframe) (*Index, error) {
	idx := New()
	for _, k := range records {
		if k.State == "" {
			k.State = StateKnown
		}
		if k.State != StateKnown && k.State != StateUnknown {
			return nil, errs.New(errs.KindIndex, "", k.ByteOffset,
				k.SegmentIndex, k.TrackID, "unknown keyframe state %q", k.State)
		}
		idx.mu.Lock()
		idx.Keyframes = append(idx.Keyframes, k)
		idx.mu.Unlock()
	}
	if err := idx.Finalize(); err != nil {
		return nil, err
	}
	return idx, nil
}
