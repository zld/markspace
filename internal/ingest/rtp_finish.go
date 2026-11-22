package ingest

import (
	"sort"

	"example.com/mediatimeline/internal/clock"
	"example.com/mediatimeline/internal/model"
)

// Finish flushes the pending unit and materializes samples in timeline order.
// Sample indices follow repaired time so the result is stable across input
// reordering.
func (a *RTPReassembler) Finish() (RTPResult, error) {
	a.flush()
	if a.repairer == nil {
		return RTPResult{}, model.NewError(a.opts.Track, model.StageParse, model.CodeInvalidParameter, false,
			"no RTP packets were supplied")
	}

	// Decode all unit timestamps in observation order to keep repair
	// decisions deterministic, then order by repaired time. A raw timestamp
	// that is entirely absent (empty payload) is still decoded so the
	// timeline stays continuous.
	type stamped struct {
		u  accessUnit
		ts model.TimeStamp
	}
	stampedUnits := make([]stamped, 0, len(a.units))
	for _, u := range a.units {
		ts, err := a.repairer.Decode(u.rawTicks)
		if err != nil {
			a.recordError(err)
			return RTPResult{}, err
		}
		stampedUnits = append(stampedUnits, stamped{u: u, ts: ts})
	}

	// Assign durations from the repaired gap to the next distinct timestamp.
	// The final unit inherits the previous unit's duration so it is a whole
	// frame rather than a zero-length tail.
	for i := range stampedUnits {
		start, err := clock.FromModel(stampedUnits[i].ts.RepairedTime)
		if err != nil {
			return RTPResult{}, err
		}
		next := start
		if i+1 < len(stampedUnits) {
			if n, err := clock.FromModel(stampedUnits[i+1].ts.RepairedTime); err == nil && n.Cmp(start) > 0 {
				next = n
			}
		}
		var dur clock.Rational
		if next.Cmp(start) > 0 {
			dur, err = next.Sub(start)
		} else if i > 0 {
			dur, err = clock.FromModel(a.samples[i-1].Duration)
		} else {
			dur = clock.Zero
		}
		if err != nil {
			return RTPResult{}, err
		}
		stampedUnits[i].u.payload = append([]byte(nil), stampedUnits[i].u.payload...)
		a.samples = append(a.samples, model.Sample{
			Index:    i,
			Track:    a.opts.Track,
			Time:     stampedUnits[i].ts,
			Duration: clock.ToModel(dur),
			Size:     uint32(len(stampedUnits[i].u.payload)),
			Sync:     true,
		})
	}

	// Sort by repaired start so later stages can assume a monotonic timeline.
	// Stable ordering keeps duplicates in arrival order.
	sort.SliceStable(a.samples, func(i, j int) bool {
		ti, _ := clock.FromModel(a.samples[i].Time.RepairedTime)
		tj, _ := clock.FromModel(a.samples[j].Time.RepairedTime)
		if c := ti.Cmp(tj); c != 0 {
			return c < 0
		}
		return a.samples[i].Index < a.samples[j].Index
	})
	for i := range a.samples {
		a.samples[i].Index = i
	}
	if len(a.samples) == 0 {
		e := model.NewError(a.opts.Track, model.StageParse, model.CodeMissingPayload, false,
			"no access units survived reassembly")
		a.res.Errors = append(a.res.Errors, e)
		a.res.Samples = a.samples
		return a.res, e
	}
	a.res.Samples = a.samples
	a.res.Decisions = append(a.res.Decisions, a.decisions...)
	a.res.Decisions = append(a.res.Decisions, a.repairer.Decisions()...)
	a.res.Errors = append(a.res.Errors, a.errors.Items...)
	for _, e := range a.res.Errors {
		if !e.Recoverable {
			return a.res, e
		}
	}
	return a.res, nil
}
