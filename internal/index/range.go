package index

import (
	"context"
	"fmt"
	"io"

	"assetpack/internal/adapter"
	"assetpack/internal/clock"
)

// PayloadRecord is an indexed record plus bytes read from the source file.
type PayloadRecord struct {
	Record
	Payload []byte `json:"-"`
}

// Alignment is an exact endpoint distance in ticks and as a fraction of a sample.
type Alignment struct {
	Ticks          clock.Ticks    `json:"ticks"`
	SampleFraction clock.Rational `json:"sample_fraction"`
	RoundedSamples int            `json:"rounded_samples"`
}

// Coverage is the actual records and bounds returned for a requested interval.
type Coverage struct {
	TrackID                string          `json:"track_id"`
	Requested              clock.Range     `json:"requested"`
	Actual                 clock.Range     `json:"actual"`
	StartAlignment         Alignment       `json:"start_alignment"`
	EndAlignment           Alignment       `json:"end_alignment"`
	MaxRoundedAlignSamples int             `json:"max_rounded_alignment_samples"`
	Records                []Record        `json:"records"`
	Preroll                []Record        `json:"preroll,omitempty"`
	Payload                []PayloadRecord `json:"-"`
}

// Reader performs bounded random-access reads against a disk index.
type Reader struct {
	Index          *Index
	Source         io.ReaderAt
	MaxWindowBytes int64
	Cache          *PageCache
}

func NewReader(idx *Index, source io.ReaderAt) *Reader {
	return &Reader{Index: idx, Source: source, MaxWindowBytes: adapter.MaxParseWindow, Cache: NewPageCache(MaxPageCacheBytes)}
}

// ReadRange returns all samples overlapping [req.From, req.To), their preroll,
// actual coverage, and exact alignment error.
func (r *Reader) ReadRange(ctx context.Context, trackID string, req clock.Range, loadPayload bool) (*Coverage, error) {
	if r == nil || r.Index == nil {
		return nil, fmt.Errorf("nil index reader")
	}
	if err := req.Validate(r.Index.Duration); err != nil {
		return nil, clock.Wrap(trackID, "range", int64(req.To), err)
	}
	tr, err := r.Index.Track(trackID)
	if err != nil {
		return nil, clock.Wrap(trackID, "track", int64(req.From), err)
	}

	chosen := []int{}
	for _, rec := range tr.Records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := rec.PTS + rec.Duration
		if rec.PTS < req.To && end > req.From {
			chosen = append(chosen, tr.byIndex[rec.Index])
		}
	}
	if len(chosen) == 0 {
		return nil, clock.Wrap(trackID, "range-read", int64(req.From),
			fmt.Errorf("%w: no sample covers %d:%d", adapter.ErrBadInput, req.From, req.To))
	}

	preSet := map[int]struct{}{}
	queue := []int{}
	for _, pos := range chosen {
		for _, p := range tr.Records[pos].Preroll {
			if _, seen := preSet[p]; !seen {
				preSet[p] = struct{}{}
				queue = append(queue, p)
			}
		}
	}
	for len(queue) > 0 {
		idx := queue[0]
		queue = queue[1:]
		pos, ok := tr.byIndex[idx]
		if !ok {
			return nil, clock.Wrap(trackID, "preroll", 0, fmt.Errorf("missing sample %d", idx))
		}
		for _, p := range tr.Records[pos].Preroll {
			if _, seen := preSet[p]; !seen {
				preSet[p] = struct{}{}
				queue = append(queue, p)
			}
		}
	}
	prerollIndices := make([]int, 0, len(preSet))
	for idx := range preSet {
		prerollIndices = append(prerollIndices, idx)
	}
	// Stable ascending order is important for decoder initialization.
	for i := 0; i < len(prerollIndices); i++ {
		for j := i + 1; j < len(prerollIndices); j++ {
			if prerollIndices[j] < prerollIndices[i] {
				prerollIndices[i], prerollIndices[j] = prerollIndices[j], prerollIndices[i]
			}
		}
	}

	cov := &Coverage{TrackID: trackID, Requested: req}
	first := tr.Records[chosen[0]]
	last := tr.Records[chosen[len(chosen)-1]]
	cov.Actual = clock.Range{From: first.PTS, To: last.PTS + last.Duration}
	cov.StartAlignment = alignment(req.From-first.PTS, first.Duration)
	cov.EndAlignment = alignment(cov.Actual.To-req.To, last.Duration)
	cov.MaxRoundedAlignSamples = maxInt(cov.StartAlignment.RoundedSamples, cov.EndAlignment.RoundedSamples)
	for _, idx := range prerollIndices {
		cov.Preroll = append(cov.Preroll, tr.Records[tr.byIndex[idx]])
	}
	for _, pos := range chosen {
		cov.Records = append(cov.Records, tr.Records[pos])
	}

	if loadPayload {
		if r.Source == nil {
			return nil, clock.Wrap(trackID, "range-read", 0, fmt.Errorf("payload requested without source reader"))
		}
		need := int64(0)
		all := append(append([]Record{}, cov.Preroll...), cov.Records...)
		for _, rec := range all {
			need += int64(rec.Size)
		}
		window := r.MaxWindowBytes
		if window <= 0 || window > adapter.MaxParseWindow {
			window = adapter.MaxParseWindow
		}
		if need > window {
			return nil, clock.Wrap(trackID, "read-window", int64(req.From),
				fmt.Errorf("range needs %d bytes, window is %d", need, window))
		}
		for _, rec := range cov.Records {
			payload, err := r.readPayload(trackID, rec)
			if err != nil {
				return nil, err
			}
			cov.Payload = append(cov.Payload, PayloadRecord{Record: rec, Payload: payload})
		}
	}
	return cov, nil
}

func (r *Reader) readPayload(trackID string, rec Record) ([]byte, error) {
	key := fmt.Sprintf("%s:%s:%d", r.Index.SourceSHA, trackID, rec.Index)
	if r.Cache != nil {
		if v, ok := r.Cache.Get(key); ok {
			if b, ok := v.([]byte); ok {
				return append([]byte(nil), b...), nil
			}
		}
	}
	b := make([]byte, rec.Size)
	if _, err := r.Source.ReadAt(b, rec.Offset); err != nil {
		return nil, clock.Wrap(trackID, "read-sample", rec.Offset,
			fmt.Errorf("%w: sample %d: %v", adapter.ErrBadInput, rec.Index, err))
	}
	if r.Cache != nil {
		r.Cache.Put(CachePage{Key: key, Bytes: rec.Size, Value: append([]byte(nil), b...)})
	}
	return b, nil
}

func alignment(delta clock.Ticks, sampleDuration clock.Ticks) Alignment {
	a := delta
	if a < 0 {
		a = -a
	}
	frac := clock.Rational{Num: int64(a), Den: int64(sampleDuration)}
	if sampleDuration > 0 {
		g := gcd64(int64(a), int64(sampleDuration))
		frac.Num /= g
		frac.Den /= g
	}
	rounded := 0
	if a != 0 {
		rounded = 1 // any sub-sample endpoint adjustment requires at most one sample
	}
	return Alignment{Ticks: delta, SampleFraction: frac, RoundedSamples: rounded}
}

func gcd64(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	if a < 0 {
		return -a
	}
	if a == 0 {
		return 1
	}
	return a
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
