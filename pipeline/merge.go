package pipeline

import (
	"sort"

	"github.com/zld/media-check/media"
)

// ManifestEntry is one ordered row of the run manifest.
type ManifestEntry struct {
	Index       int         `json:"index"`
	TrackID     uint32      `json:"track_id"`
	Status      Status      `json:"status"`
	FirstSample uint32      `json:"first_sample"`
	SampleCount uint32      `json:"sample_count"`
	DTSStart    uint64      `json:"dts_start"`
	Duration    uint64      `json:"duration"`
	Bytes       int64       `json:"bytes"`
	SHA256      string      `json:"sha256,omitempty"`
	Artifact    string      `json:"artifact,omitempty"`
	ArtifactSHA string      `json:"artifact_sha256,omitempty"`
	Resumed     bool        `json:"resumed"`
	ErrClass    media.Class `json:"error_class,omitempty"`
	Err         string      `json:"error,omitempty"`
}

// Manifest is the deterministic output of a run. Entries are always ordered by
// source task index, regardless of worker completion order.
type Manifest struct {
	Version   int             `json:"version"`
	Seed      int64           `json:"seed"`
	Source    string          `json:"source"`
	SourceSHA string          `json:"source_sha256"`
	Entries   []ManifestEntry `json:"entries"`
	Stats     PoolStats       `json:"pool"`
	RTP       *RTPReport      `json:"rtp,omitempty"`
	Index     *IndexReport    `json:"index,omitempty"`
	Tracks    int             `json:"tracks"`
	Segments  int             `json:"segments"`
	Cancelled int             `json:"cancelled_tasks"`
	Failed    int             `json:"failed_tasks"`
	Complete  bool            `json:"complete"`
	SHA256    string          `json:"manifest_sha256,omitempty"`
}

// RTPReport summarizes receive-side transport counters.
type RTPReport struct {
	Received      int         `json:"received"`
	Duplicates    int         `json:"duplicates"`
	Reordered     int         `json:"reordered"`
	LateArrived   int         `json:"late_arrived"`
	Lost          int         `json:"lost"`
	Recovered     int         `json:"recovered"`
	Gaps          []GapRecord `json:"gaps,omitempty"`
	MinBps        float64     `json:"min_bps"`
	MeanBps       float64     `json:"mean_bps"`
	MaxBps        float64     `json:"max_bps"`
	FinalParallel int         `json:"final_parallel"`
	FinalQueue    int         `json:"final_queue"`
	LossFree      bool        `json:"loss_free"`
}

// GapRecord is the serialized form of one transport gap.
type GapRecord struct {
	Start uint64 `json:"start"`
	End   uint64 `json:"end"`
	State string `json:"state"`
}

// IndexReport summarizes keyframe index verification.
type IndexReport struct {
	SHA       string  `json:"sha256"`
	Keyframes int     `json:"keyframes"`
	HitRatio  float64 `json:"hit_ratio"`
	Checks    int     `json:"checks"`
}

// Merger accumulates results and emits them in task-index order. It is safe for
// a single consumer goroutine; workers never touch it directly.
type Merger struct {
	total  int
	seen   map[int]Result
	order  []ManifestEntry
	source string
	srcSHA string
	seed   int64
	stats  PoolStats
}

// NewMerger prepares a merger sized for total tasks.
func NewMerger(total int, source, sourceSHA string, seed int64) (*Merger, error) {
	if total < 0 {
		return nil, media.NewError(media.ClassInvalidArgument, "merge", 0, "negative task count %d", total)
	}
	return &Merger{total: total, seen: make(map[int]Result, total), source: source, srcSHA: sourceSHA, seed: seed}, nil
}

// Add records one result. Duplicate indices are rejected so a misbehaving
// worker cannot silently overwrite a success with a failure.
func (m *Merger) Add(r Result) error {
	if _, dup := m.seen[r.Index]; dup {
		return media.NewError(media.ClassIntegrity, "merge.dup", 0, "duplicate result index %d", r.Index)
	}
	if r.Status == "" {
		r.Status = StatusOK
	}
	if r.Err != nil && r.ErrClass == "" {
		r.ErrClass = media.ClassOf(r.Err)
	}
	if r.Err != nil && r.ErrText == "" {
		r.ErrText = r.Err.Error()
	}
	m.seen[r.Index] = r
	return nil
}

// SetStats stores the final pool statistics.
func (m *Merger) SetStats(s PoolStats) { m.stats = s }

// Results returns results ordered by index once all are present.
func (m *Merger) Results() ([]Result, error) {
	out := make([]Result, 0, len(m.seen))
	idx := make([]int, 0, len(m.seen))
	for i := range m.seen {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for _, i := range idx {
		out = append(out, m.seen[i])
	}
	return out, nil
}

// Missing returns the indices that never produced a result, sorted.
func (m *Merger) Missing() []int {
	var out []int
	for i := 0; i < m.total; i++ {
		if _, ok := m.seen[i]; !ok {
			out = append(out, i)
		}
	}
	return out
}
