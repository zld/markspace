package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"rtpregress/internal/audio"
)

// MergedSegment is one entry of the deterministic, timeline-ordered output.
type MergedSegment struct {
	SegNumber   int
	PID         uint16
	StartNanos  int64
	StartSample int64
	Samples     int
	PCMSHA256   string
	PCM         []byte
}

// Merger collects worker results and emits a total order independent of the
// number of workers or their scheduling. The sort key is (start sample, PID,
// segment number), matching the audio planner's tie-breaking rules.
type Merger struct {
	items []MergedSegment
}

// NewMerger creates an empty merger.
func NewMerger() *Merger { return &Merger{} }

// Add folds one completed pool result into the merge buffer.
func (m *Merger) Add(r PoolResult, placement audio.Placement) {
	sum := sha256.Sum256(r.Result.PCM)
	m.items = append(m.items, MergedSegment{
		SegNumber:   r.Job.SegNumber,
		PID:         r.Job.PID,
		StartNanos:  r.Result.StartNanos,
		StartSample: placement.StartSample,
		Samples:     r.Result.Samples,
		PCMSHA256:   hex.EncodeToString(sum[:]),
		PCM:         r.Result.PCM,
	})
}

// less defines the stable total ordering.
func less(a, b MergedSegment) bool {
	if a.StartSample != b.StartSample {
		return a.StartSample < b.StartSample
	}
	if a.PID != b.PID {
		return a.PID < b.PID
	}
	return a.SegNumber < b.SegNumber
}

// Sorted returns the segments in deterministic order. The slice is freshly
// allocated so callers cannot mutate internal ordering state.
func (m *Merger) Sorted() []MergedSegment {
	out := make([]MergedSegment, len(m.items))
	copy(out, m.items)
	sort.SliceStable(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
}

// Len reports how many segments have been merged.
func (m *Merger) Len() int { return len(m.items) }

// ManifestHash hashes the ordered segment coordinates and PCM digests. It is
// identical for any worker count that produces the same completed segments.
func (m *Merger) ManifestHash() string {
	ordered := m.Sorted()
	h := sha256.New()
	for _, s := range ordered {
		var buf [8]byte
		put64(buf[:], uint64(s.StartSample))
		h.Write(buf[:])
		put64(buf[:], uint64(s.PID))
		h.Write(buf[:])
		put64(buf[:], uint64(s.SegNumber))
		h.Write(buf[:])
		h.Write([]byte(s.PCMSHA256))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func put64(b []byte, v uint64) {
	for i := 7; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
}
