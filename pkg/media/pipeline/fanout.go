package pipeline

import (
	"context"
	"runtime"
	"sync"

	"cmafrepair/pkg/media/clock"
	"cmafrepair/pkg/media/cmaf"
	"cmafrepair/pkg/media/timebase"
)

// FanOut parses and repairs every representation concurrently, returning one
// RepResult per input in the same order. Workers are bounded by
// Options.MaxConcurrent; each representation is fully isolated so one bad
// rendition never aborts the others.
func FanOut(ctx context.Context, inputs []RepInput, opt Options) []RepResult {
	workers := opt.MaxConcurrent
	if workers <= 0 {
		workers = DefaultOptions().MaxConcurrent
	}
	if workers > len(inputs) {
		workers = len(inputs)
	}
	if workers < 1 {
		workers = 1
	}
	if max := runtime.GOMAXPROCS(0); max > 0 && workers > max {
		workers = max
	}

	results := make([]RepResult, len(inputs))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup

	for i := range inputs {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			// Context cancelled: mark remaining inputs and stop scheduling.
			for j := i; j < len(inputs); j++ {
				results[j] = fatalResult(inputs[j], timebase.AsMedia(ctx.Err()))
			}
			wg.Wait()
			return results
		}
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := ctx.Err(); err != nil {
				results[idx] = fatalResult(inputs[idx], timebase.AsMedia(err))
				return
			}
			results[idx] = repairOne(inputs[idx])
		}(i)
	}
	wg.Wait()
	return results
}

// repairOne runs the complete parse+repair path for a single representation.
// It never escapes as a panic: every failure becomes an isolated RepResult.
func repairOne(in RepInput) (out RepResult) {
	out.Name = in.Name
	defer func() {
		if rec := recover(); rec != nil {
			out.Fatal = timebase.NewError(timebase.CodeInputInvalid, timebase.PhaseRepair,
				"representation %s panicked: %v", in.Name, rec)
			out.Isolated = true
		}
	}()

	init, err := cmaf.ParseInit(in.InitPath)
	if err != nil {
		return fatalResult(in, timebase.AsMedia(err))
	}
	out.Init = init

	// Derive the clock anchor and count RTP wrap/rollback events.
	anchor, wraps, rollbacks := buildAnchor(in, init)
	out.Anchor = anchor
	out.Wraps = wraps
	out.Rollbacks = rollbacks

	repair := cmaf.NewTimelineRepair(in.Name, init)
	frags := make([]*cmaf.Fragment, 0, len(in.FragmentPaths))
	for _, fp := range in.FragmentPaths {
		f, perr := cmaf.ParseFragment(fp)
		if perr != nil {
			// Hard-unreadable fragment: record and stop this rep only.
			out.Fatal = timebase.AsMedia(perr).WithContext(in.Name, cmaf.FragmentName(fp))
			out.Isolated = true
			out.Fragments = frags
			out.Defects = collectDefects(frags, nil)
			return out
		}
		if _, aerr := repair.Add(f); aerr != nil {
			out.Fatal = timebase.AsMedia(aerr).WithContext(in.Name, f.Name)
			out.Isolated = true
			out.Fragments = frags
			return out
		}
		frags = append(frags, f)
	}
	out.Fragments = frags
	out.Timeline = repair.Build()
	out.Defects = collectDefects(frags, out.Timeline)
	return out
}

// buildAnchor resolves the representation anchor and runs the 32-bit RTP
// timestamp stream through the unwrapper for wrap/rollback statistics.
func buildAnchor(in RepInput, init *cmaf.InitInfo) (clock.ClockAnchor, int, int) {
	hz := in.ClockHz
	if hz == 0 {
		hz = init.Timescale
	}
	wraps, rollbacks := countRtpEvents(in.RTPTimestamps, hz)
	if in.Anchor != nil {
		a := *in.Anchor
		if a.ClockHz == 0 {
			a.ClockHz = hz
		}
		return a, wraps, rollbacks
	}
	// No explicit anchor: synthesize one at the origin (media 0 == shared 0).
	return clock.ClockAnchor{ClockHz: hz}, wraps, rollbacks
}

func countRtpEvents(ts []uint32, hz uint32) (wraps, rollbacks int) {
	if len(ts) == 0 {
		return 0, 0
	}
	u := clock.NewUnwrapper(hz)
	for _, t := range ts {
		if _, _, err := u.Observe(t); err != nil {
			break
		}
	}
	return u.Wraps(), u.Rollbacks()
}

func fatalResult(in RepInput, e *timebase.MediaError) RepResult {
	return RepResult{Name: in.Name, Fatal: e, Isolated: true}
}
