package main

import (
	"context"
	"fmt"

	"mediarun/internal/audio"
	"mediarun/internal/engine"
	"mediarun/internal/fixture"
	"mediarun/internal/pipeline"
)

// RunReport is the terminal summary of a `run` invocation.
type RunReport struct {
	Fixture         string
	Interrupted     bool
	SegmentsNow     int
	SegmentsResumed int
	DuplicateSegs   int
	SegmentsTotal   int
	FinalDigest     string
	CacheHits       int
	CacheMisses     int
	OutputPath      string
}

// executeRun drives one run invocation against a generated fixture.
func executeRun(ctx context.Context, n *audio.Normalizer, kind, checkpointDir string,
	interruptAfter int) (*RunReport, error) {
	spec := fixture.DefaultSpec(kind)
	st := fixture.Build(spec)

	var rep *engine.IngestReport
	var err error
	if spec.LossyRTP {
		rep, err = engine.IngestRTP(st.RTP, 8, fixture.AudioPID, fixture.VideoPID)
	} else {
		rep, err = engine.IngestTS(st.TS, fixture.AudioPID, fixture.VideoPID)
	}
	if err != nil {
		return nil, err
	}

	cfg := engine.Config{
		JobID:          "job-" + kind,
		InputDigest:    pipeline.DigestSHA256(st.TS),
		InputSize:      int64(len(st.TS)),
		PacketCount:    rep.Packets,
		InputPath:      "self://" + kind,
		WorkDir:        checkpointDir + "/work",
		CheckpointPath: checkpointDir + "/checkpoint.json",
		CacheDir:       checkpointDir + "/cache",
		InterruptAfter: interruptAfter,
	}
	res, err := engine.NewRunner(n).Run(ctx, rep, cfg)
	if err != nil && err != engine.ErrInterrupted {
		return nil, err
	}
	return &RunReport{
		Fixture: kind, Interrupted: res.Interrupted,
		SegmentsNow: res.SegmentsNow, SegmentsResumed: res.SegmentsResumed,
		DuplicateSegs: res.DuplicateSegment, SegmentsTotal: res.SegmentsTotal,
		FinalDigest: res.FinalDigest, CacheHits: res.CacheHits, CacheMisses: res.CacheMisses,
		OutputPath: res.OutputPath,
	}, nil
}

func (r RunReport) String() string {
	state := "completed"
	if r.Interrupted {
		state = "interrupted"
	}
	return fmt.Sprintf(
		"run fixture=%s state=%s segments_now=%d segments_resumed=%d duplicate_segments=%d "+
			"segments_total=%d cache_hits=%d cache_misses=%d digest=%s output=%s",
		r.Fixture, state, r.SegmentsNow, r.SegmentsResumed, r.DuplicateSegs,
		r.SegmentsTotal, r.CacheHits, r.CacheMisses, r.FinalDigest, r.OutputPath)
}
