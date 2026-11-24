package main

import (
	"context"
	"fmt"
	"path/filepath"

	"mediarun/internal/audio"
	"mediarun/internal/engine"
	"mediarun/internal/fixture"
	"mediarun/internal/mp4info"
	"mediarun/internal/pipeline"
)

// FixtureReport is the machine- and human-readable result for one scenario.
type FixtureReport struct {
	Kind             string
	SyncErrors       int
	RecoveredPackets int
	LossRecoveryRate float64
	AVSyncMS         int64
	CacheHits        int
	CacheMisses      int
	ResumeMatches    bool
	ProvenanceValid  bool
	P95MS            int64
	Segments         int
}

// runOne ingests a generated stream and processes it under one work directory.
func runOne(ctx context.Context, r *engine.Runner, kind, outRoot, cacheDir string,
	interruptAfter int) (*fixture.Stream, *engine.RunResult, *engine.IngestReport, error) {
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
		return st, nil, rep, err
	}

	dir := filepath.Join(outRoot, "work-"+kind)
	cfg := engine.Config{
		JobID:          "job-" + kind,
		InputDigest:    pipeline.DigestSHA256(st.TS),
		InputSize:      int64(len(st.TS)),
		PacketCount:    rep.Packets,
		InputPath:      "self://" + kind,
		WorkDir:        filepath.Join(dir, "work"),
		CheckpointPath: filepath.Join(dir, "checkpoint.json"),
		CacheDir:       cacheDir,
		InterruptAfter: interruptAfter,
	}
	res, err := r.Run(ctx, rep, cfg)
	return st, res, rep, err
}

// verifyFixture runs one scenario twice (cold then warm) and, where relevant,
// a resume run, then assembles its acceptance report.
func verifyFixture(ctx context.Context, n *audio.Normalizer, kind, outRoot string) (*FixtureReport, error) {
	r := engine.NewRunner(n)
	// One shared cache directory across cold/warm/resume runs for this kind.
	sharedCache := filepath.Join(outRoot, "shared-cache")

	// Cold run: populates the shared cache.
	_, cold, rep, err := runOne(ctx, r, kind, outRoot, sharedCache, 0)
	if err != nil {
		return nil, fmt.Errorf("%s cold run: %w", kind, err)
	}

	// Warm run: same cache, fresh work dir/checkpoint. Every segment must hit.
	r2 := engine.NewRunner(n)
	_, warm, _, err := runOne(ctx, r2, kind, filepath.Join(outRoot, "warm"), sharedCache, 0)
	if err != nil {
		return nil, fmt.Errorf("%s warm run: %w", kind, err)
	}

	// Resume run: interrupt then continue; final digest must equal the cold run.
	r3 := engine.NewRunner(n)
	_, first, _, ierr := runOne(ctx, r3, kind, filepath.Join(outRoot, "resume"), sharedCache, 2)
	if ierr != nil && ierr != engine.ErrInterrupted {
		return nil, fmt.Errorf("%s interrupt run: %w", kind, ierr)
	}
	r4 := engine.NewRunner(n)
	_, resumed, _, rerr := runOne(ctx, r4, kind, filepath.Join(outRoot, "resume"), sharedCache, 0)
	if rerr != nil {
		return nil, fmt.Errorf("%s resume run: %w", kind, rerr)
	}
	resumeMatches := first != nil && resumed != nil &&
		resumed.FinalDigest == cold.FinalDigest && resumed.DuplicateSegment == 0

	prov, perr := pipeline.LoadProvenance(filepath.Join(outRoot, "work-"+kind, "work", "provenance.json"))
	provValid := perr == nil && prov.Valid

	report := &FixtureReport{
		Kind: kind, SyncErrors: rep.SyncErrors,
		RecoveredPackets: rep.RecoveredPackets, LossRecoveryRate: rep.LossRecoveryRate,
		AVSyncMS: cold.AVSyncMS, CacheHits: warm.CacheHits, CacheMisses: warm.CacheMisses,
		ResumeMatches: resumeMatches, ProvenanceValid: provValid,
		P95MS: cold.P95ProcessMS, Segments: cold.SegmentsTotal,
	}
	return report, nil
}

// probeOutputMP4 exercises go-mp4 on an MP4 rendering of the combined output,
// proving the reusable timing/sample path against a real container.
func probeOutputMP4(ctx context.Context, n *audio.Normalizer, wavPath, mp4Path string) error {
	if err := encodeAAC(ctx, n, wavPath, mp4Path); err != nil {
		return err
	}
	info, err := mp4info.Read(mp4Path)
	if err != nil {
		return err
	}
	if info.SampleCount == 0 || info.Timescale == 0 {
		return fmt.Errorf("mp4 probe returned empty timing structure")
	}
	return nil
}
