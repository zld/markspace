// Package merge reorders worker results back into a deterministic delivery
// order. Sorting keys are the segment's sample start and, for ties, the
// producer-assigned input sequence. Partial worker failures never discard
// successful work; every failure is retained with its reason.
package merge

import (
	"sort"

	"mediad/media"
	"mediad/pool"
)

// Item is one merged output row.
type Item struct {
	Sequence    int    `json:"sequence"`
	SampleStart int64  `json:"sample_start"`
	SampleEnd   int64  `json:"sample_end"`
	OK          bool   `json:"ok"`
	Value       any    `json:"value,omitempty"`
	Failure     string `json:"failure,omitempty"`
}

// Order is the merge outcome. Successful items are sorted; failures are kept
// in arrival order so a re-run with the same inputs renders identically.
type Order struct {
	Items    []Item       `json:"items"`
	Failures []Item       `json:"failures"`
	Faults   []media.Fault `json:"faults"`
}

// keyed binds a pool result to the timeline coordinates supplied at submit
// time. The pool only carries an opaque sequence; the producer knows where
// that sequence sits on the sample axis.
type keyed struct {
	res   pool.Result
	start int64
	end   int64
}

// Merger collects results until Collect is called. It is single-writer safe:
// the pipeline goroutine feeding results is the only writer.
type Merger struct {
	received []keyed
	coords   map[int][2]int64
}

// NewMerger builds an empty merger. coords maps task sequence to
// [sampleStart, sampleEnd]; it is populated by the producer before submission.
func NewMerger(coords map[int][2]int64) *Merger {
	copied := make(map[int][2]int64, len(coords))
	for k, v := range coords {
		copied[k] = v
	}
	return &Merger{coords: copied}
}

// Ingest stores one worker result. Unknown sequences are still accepted and
// sorted by sequence alone so no result is ever silently dropped.
func (m *Merger) Ingest(r pool.Result) {
	var start, end int64
	if c, ok := m.coords[r.Sequence]; ok {
		start, end = c[0], c[1]
	}
	m.received = append(m.received, keyed{res: r, start: start, end: end})
}

// Collect produces the stable order. The input slice is not mutated.
func (m *Merger) Collect() *Order {
	out := &Order{}
	entries := make([]keyed, len(m.received))
	copy(entries, m.received)

	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.start != b.start {
			return a.start < b.start
		}
		return a.res.Sequence < b.res.Sequence
	})

	for _, e := range entries {
		item := Item{
			Sequence:    e.res.Sequence,
			SampleStart: e.start,
			SampleEnd:   e.end,
		}
		if e.res.Err != nil {
			item.OK = false
			item.Failure = e.res.Err.Error()
			out.Failures = append(out.Failures, item)
			out.Faults = append(out.Faults, media.NewSampleFault(
				media.CatWorkerFailed, e.start, e.end,
				"sequence %d: %v", e.res.Sequence, e.res.Err))
			continue
		}
		item.OK = true
		item.Value = e.res.Value
		out.Items = append(out.Items, item)
	}
	// Successful items inherit the sorted order; failures get a canonical
	// sequence order so reruns are identical regardless of completion timing.
	sort.SliceStable(out.Failures, func(i, j int) bool {
		return out.Failures[i].Sequence < out.Failures[j].Sequence
	})
	sortFaults(out.Faults)
	return out
}

// sortFaults keeps fault ordering canonical across runs.
func sortFaults(faults []media.Fault) {
	sort.SliceStable(faults, func(i, j int) bool {
		if faults[i].SampleStart != faults[j].SampleStart {
			return faults[i].SampleStart < faults[j].SampleStart
		}
		return faults[i].Message < faults[j].Message
	})
}
