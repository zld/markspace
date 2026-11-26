package recover

import (
	"errors"
	"fmt"
	"sort"

	"mediad/internal/cache"
	"mediad/internal/model"
)

// ErrSourceCorrupt is returned by a Processor when the underlying source bytes
// are unusable (as opposed to a failed checksum on a previously derived
// segment). The range is then recorded as unrecoverable instead of retried.
var ErrSourceCorrupt = errors.New("recover: source bytes are corrupt")

// Processor recomputes one segment. Implementations run the actual bounded
// derivation; returning ErrSourceCorrupt marks the range unrecoverable.
type Processor interface {
	Process(seg Segment) (data []byte, err error)
}

// Result summarizes one recovery pass for stable reporting.
type Result struct {
	Recovered     []int           `json:"recovered_segment_ids"`
	Unrecoverable []int           `json:"unrecoverable_segment_ids"`
	CacheHits     []int           `json:"cache_hit_segment_ids"`
	Anomalies     []model.Anomaly `json:"anomalies"`
}

// Recoverer drives cache-first, range-scoped recovery over a segment set.
type Recoverer struct {
	store *cache.Store
	proc  Processor
}

// New builds a Recoverer over a derived cache and segment processor.
func New(store *cache.Store, proc Processor) *Recoverer {
	return &Recoverer{store: store, proc: proc}
}

// Run processes every segment in the set. Each segment is independently
// bounded by a sync sample, so a failure isolates exactly that segment and the
// recomputation never touches healthy neighbours.
func (r *Recoverer) Run(set *SegmentSet, known map[int]model.Anomaly) (*Result, error) {
	if set == nil {
		return nil, errors.New("recover: nil segment set")
	}
	res := &Result{}
	bad := make(map[int]model.Anomaly)
	for id, a := range known {
		bad[id] = a
	}

	for i := range set.Segments {
		seg := set.Segments[i]
		// A segment with no recorded fault is served from cache when present.
		if _, isBad := bad[seg.ID]; !isBad {
			if r.store.Has(seg.CacheKey) {
				res.CacheHits = append(res.CacheHits, seg.ID)
				continue
			}
			// No fault and no cache yet: derive normally.
			if err := r.derive(seg, res); err != nil {
				return nil, err
			}
			continue
		}

		// A faulty segment: recompute only this range from its sync boundary.
		anomaly := bad[seg.ID]
		anomaly.TrackID = seg.TrackID
		anomaly.StartNanos = seg.StartNanos
		anomaly.EndNanos = seg.EndNanos
		anomaly.SourceOffset = seg.Offset
		anomaly.SourceLength = seg.Length
		anomaly.ID = fmt.Sprintf("anomaly-%d-%d", seg.TrackID, seg.ID)

		data, err := r.proc.Process(seg)
		if errors.Is(err, ErrSourceCorrupt) {
			anomaly.Recovery = model.StateUnrecoverable
			anomaly.Message = messageOr(anomaly.Message, "source corrupt: range unrecoverable")
			res.Unrecoverable = append(res.Unrecoverable, seg.ID)
			res.Anomalies = append(res.Anomalies, anomaly)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("recover: segment %d: %w", seg.ID, err)
		}
		if err := r.store.Put(seg.CacheKey, data); err != nil {
			return nil, err
		}
		anomaly.Recovery = model.StateRecovered
		anomaly.Checksum = shortHash(data)
		res.Recovered = append(res.Recovered, seg.ID)
		res.Anomalies = append(res.Anomalies, anomaly)
	}

	sort.Ints(res.Recovered)
	sort.Ints(res.Unrecoverable)
	sort.Ints(res.CacheHits)
	sort.SliceStable(res.Anomalies, func(i, j int) bool {
		return res.Anomalies[i].StableKey() < res.Anomalies[j].StableKey()
	})
	return res, nil
}

// derive computes a healthy, cache-missing segment and stores it.
func (r *Recoverer) derive(seg Segment, res *Result) error {
	data, err := r.proc.Process(seg)
	if err != nil {
		if errors.Is(err, ErrSourceCorrupt) {
			res.Unrecoverable = append(res.Unrecoverable, seg.ID)
			res.Anomalies = append(res.Anomalies, model.Anomaly{
				ID:   fmt.Sprintf("anomaly-%d-%d", seg.TrackID, seg.ID),
				Type: model.AnomalyUnrecoverable, TrackID: seg.TrackID,
				StartNanos: seg.StartNanos, EndNanos: seg.EndNanos,
				SourceOffset: seg.Offset, SourceLength: seg.Length,
				Message:  "source corrupt: range unrecoverable",
				Recovery: model.StateUnrecoverable,
			})
			return nil
		}
		return fmt.Errorf("recover: derive segment %d: %w", seg.ID, err)
	}
	return r.store.Put(seg.CacheKey, data)
}

func messageOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// shortHash is a stable checksum label for manifest output (full SHA-256 hex).
func shortHash(data []byte) string {
	return cache.SumHex(data)
}
