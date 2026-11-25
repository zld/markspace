package continuity

import (
	"context"
	"fmt"
	"path/filepath"
)

// The recovery scenario demonstrates checkpointing around late data: a first
// pass commits a prefix and writes a checkpoint, the process is treated as
// interrupted, and a second pass restores the checkpoint, replays within the
// late window (recomputing exactly once) and rejects anything beyond it. Both
// passes must yield the same summary for identical input.

// runLateCheckpoint executes the recovery scenario end to end.
func runLateCheckpoint(ctx context.Context, cfg EngineConfig) (*Report, error) {
	if cfg.WorkDir == "" {
		return nil, Usagef("late-with-checkpoint requires a working directory")
	}
	runs, err := loadSources(cfg)
	if err != nil {
		return nil, err
	}
	sources, initByLabel, err := demuxInits(cfg, runs)
	if err != nil {
		return nil, err
	}
	tm, err := BuildTrackMap(sources)
	if err != nil {
		return nil, err
	}
	tcfg := timelineConfigFor(cfg)
	// Pass 1: the first two thirds of main's samples commit, then the
	// checkpoint is written. Backup data is withheld for pass 2, exactly like an
	// interrupted switchover where the backup segments had not arrived yet.
	first, err := NewTimeline(tm, tcfg)
	if err != nil {
		return nil, err
	}
	arrivals, err := collectArrivals(ctx, cfg, runs, initByLabel)
	if err != nil {
		return nil, err
	}
	if len(arrivals) == 0 {
		return nil, NewError(KindParse, Location{Offset: -1, Sample: -1}, "no samples to process")
	}
	split := 0
	for split < len(arrivals) && arrivals[split].source == "main" && split < 4 {
		split++
	}
	for _, a := range arrivals[:split] {
		if err := first.Add(a.source, a.sample); err != nil {
			return nil, err
		}
	}
	cp, err := NewCheckpoint(CheckpointConfig{RunID: "late", TrackMapHash: tm.Hash()}, first)
	if err != nil {
		return nil, err
	}
	cpPath := filepath.Join(cfg.WorkDir, "checkpoint.json")
	if err := SaveCheckpoint(cpPath, cp); err != nil {
		return nil, err
	}
	recoveredTrail := append([]string{fmt.Sprintf("checkpoint written after %d samples", len(cp.Samples))}, "")

	// Simulated interruption: the first timeline is discarded.
	loaded, err := LoadCheckpoint(cpPath)
	if err != nil {
		return nil, err
	}
	second, err := RestoreCheckpoint(loaded, tm, tcfg)
	if err != nil {
		return nil, err
	}
	// Verify the replay prefix matches the recorded input exactly.
	if idx, err := VerifyInputPrefix(loaded, arrivalsToSamples(arrivals)); err != nil {
		return nil, WrapError(err, KindCheckpoint, Location{Offset: -1, Sample: -1}, "checkpoint prefix conflict at %d", idx)
	}

	rep := &Report{TrackMapDigest: tm.Hash(), Formats: cfg.Format, Recovered: true}
	rem := arrivals[split:]
	for _, a := range rem {
		if err := second.Add(a.source, a.sample); err != nil {
			if IsKind(err, KindLate) {
				continue
			}
			return nil, err
		}
	}
	prog, err := second.Recompute()
	if err != nil {
		return nil, err
	}
	fin, err := finishReport(rep, second, tm)
	if err != nil {
		return nil, err
	}
	fin.LateRecomputed = prog.Stats.LateRecomputed
	fin.LateRejected = prog.Stats.LateRejected
	fin.Lines = append(recoveredTrail, fin.Lines...)
	fin.Lines = append(fin.Lines, fmt.Sprintf("resumed from %s: late_recomputed=%d late_rejected=%d",
		cpPath, fin.LateRecomputed, fin.LateRejected))
	return fin, nil
}

func arrivalsToSamples(in []arrival) []Sample {
	out := make([]Sample, 0, len(in))
	for _, a := range in {
		out = append(out, a.sample)
	}
	return out
}

// collectArrivals demuxes everything in deterministic order without a timeline.
func collectArrivals(ctx context.Context, cfg EngineConfig, runs []*sourceRun, initByLabel map[string]map[string]*InitSegment) ([]arrival, error) {
	q, err := NewQueue(ctx, QueueConfig{MaxBytes: cfg.QueueBytes})
	if err != nil {
		return nil, err
	}
	defer q.Close()
	go func() {
		defer q.Close()
		for _, r := range runs {
			for _, ref := range r.manifest.Segments() {
				data, err := readSegment(cfg.Root, ref)
				if err != nil {
					return
				}
				init := initByLabel[r.label][ref.InitURI]
				if init == nil {
					return
				}
				seg, err := ParseSegment(init, ref.URI, data)
				if err != nil {
					return
				}
				for _, s := range seg.Samples {
					if err := q.Push(s); err != nil {
						return
					}
				}
			}
		}
	}()
	var out []arrival
	for {
		s, err := q.Pop()
		if err != nil {
			break
		}
		out = append(out, arrival{source: sourceOf(s, runs), sample: s})
	}
	sortArrivals(out, cfg)
	return out, nil
}
