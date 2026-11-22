package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"mediamerge/internal/clock"
	"mediamerge/internal/container"
	"mediamerge/internal/fixture"
	"mediamerge/internal/manifest"
	"mediamerge/internal/txn"
)

// errCrash simulates an interrupted run for --resume.
var errCrash = errors.New("simulated crash")

type config struct {
	seed          int64
	targetBps     int64
	jitterWindow  uint64
	faultSet      string
	resume        bool
	repeat        int
	checkManifest bool
}

type faultCount struct {
	Stage string
	Kind  string
	Count int
}

type report struct {
	stages    []string
	faults    []faultCount
	m         *manifest.Manifest
	rollbacks int
	provSHA   string
}

// runPipeline executes all stages inside workDir. When stopAfter names a
// stage, the pipeline aborts right after committing it (crash simulation).
func runPipeline(cfg config, workDir string, stopAfter string) (*report, error) {
	rep := &report{}
	inputsDir := filepath.Join(workDir, "inputs")
	if err := os.MkdirAll(inputsDir, 0o755); err != nil {
		return nil, err
	}
	for _, d := range []string{"tmp", "out"} {
		if err := os.MkdirAll(filepath.Join(workDir, d), 0o755); err != nil {
			return nil, err
		}
	}
	fs, err := fixture.Generate(inputsDir, fixture.Options{
		Seed: cfg.seed, TargetBps: cfg.targetBps, Duration: 2, FaultSet: cfg.faultSet,
	})
	if err != nil {
		return nil, err
	}
	prov := txn.NewProvenance(map[string]string{
		"seed": fmt.Sprint(cfg.seed), "target_bps": fmt.Sprint(cfg.targetBps),
		"jitter_window": fmt.Sprint(cfg.jitterWindow), "fault_set": cfg.faultSet,
	})
	inputFiles := []string{fs.NormalMP4, fs.TruncMP4, fs.OobMP4, fs.RTPNormal, fs.RTPFault}
	digests := map[string]string{}
	for _, f := range inputFiles {
		d, err := sha256File(f)
		if err != nil {
			return nil, err
		}
		digests[f] = d
		prov.AddInput(filepath.Base(f), d)
	}
	coord, err := txn.NewCoordinator(workDir)
	if err != nil {
		return nil, err
	}
	defer coord.Close()
	stageInputs := func(files ...string) []txn.InputDigest {
		var out []txn.InputDigest
		for _, f := range files {
			out = append(out, txn.InputDigest{Name: filepath.Base(f), SHA256: digests[f]})
		}
		return out
	}
	crashed := func(stage string) (bool, error) {
		if stopAfter == stage {
			return true, errCrash
		}
		return false, nil
	}

	// ---- stage: container ----
	lim := container.DefaultLimits()
	fi, err := container.ParseFile(fs.NormalMP4, lim)
	if err != nil {
		return nil, fmt.Errorf("media: %w", err)
	}
	tables := map[uint32]*container.SampleTable{}
	for i := range fi.Tracks {
		st, err := container.ExpandSamples(&fi.Tracks[i], fi.Size, lim)
		if err != nil {
			return nil, fmt.Errorf("media: %w", err)
		}
		tables[fi.Tracks[i].TrackID] = st
	}
	for _, bad := range []struct{ path, kind string }{
		{fs.TruncMP4, "truncated-box"}, {fs.OobMP4, "oob-sample-table"},
	} {
		_, err := container.ParseFile(bad.path, lim)
		if err == nil {
			// Parseable container: force sample-table validation to surface
			// out-of-bounds offsets.
			var fi2 *container.FileInfo
			fi2, err = container.ParseFile(bad.path, lim)
			if err == nil {
				for i := range fi2.Tracks {
					_, err = container.ExpandSamples(&fi2.Tracks[i], fi2.Size, lim)
					if err != nil {
						break
					}
				}
			}
		}
		if err == nil {
			return nil, fmt.Errorf("media: fault fixture %s unexpectedly accepted", bad.path)
		}
		var be *container.BoxError
		if !errors.As(err, &be) {
			return nil, fmt.Errorf("media: unexpected fault error: %w", err)
		}
		rep.faults = append(rep.faults, faultCount{Stage: "container", Kind: bad.kind, Count: 1})
		prov.AddDecision("container", "reject-input", fmt.Sprintf("%s: %s", bad.kind, be))
	}
	if done, err := coord.Done("container", stageInputs(fs.NormalMP4)); err != nil {
		prov.InputChanges = append(prov.InputChanges, "container")
	} else if !done {
		coord.RecordInputs("container", stageInputs(fs.NormalMP4))
		coord.Begin("container")
		if err := coord.Commit("container", stageInputs(fs.NormalMP4), digestOf(fi.Tracks), nil,
			func() error { return nil }); err != nil {
			return nil, err
		}
	}
	rep.stages = append(rep.stages, "container")
	if _, err := crashed("container"); err != nil {
		return rep, err
	}

	// ---- stage: transport ----
	rtpFile := fs.RTPNormal
	if cfg.faultSet == "all" || cfg.faultSet == "rtp" {
		rtpFile = fs.RTPFault
	}
	pkts, tstats, err := readRTPStream(rtpFile, cfg.jitterWindow)
	if err != nil {
		return nil, err
	}
	if tstats.Duplicates > 0 {
		rep.faults = append(rep.faults, faultCount{Stage: "transport", Kind: "duplicate-packets", Count: tstats.Duplicates})
		prov.AddDecision("transport", "drop-duplicates", fmt.Sprintf("%d idempotent drops", tstats.Duplicates))
	}
	if tstats.Lost > 0 {
		rep.faults = append(rep.faults, faultCount{Stage: "transport", Kind: "lost-packets", Count: tstats.Lost})
		prov.AddDecision("transport", "declare-gap",
			fmt.Sprintf("%d packets unrecoverable in %d gaps", tstats.Lost, len(tstats.Gaps)))
	}
	if tstats.Recovered > 0 {
		prov.AddDecision("transport", "recover-reordered", fmt.Sprintf("%d packets", tstats.Recovered))
	}
	if done, err := coord.Done("transport", stageInputs(rtpFile)); err != nil {
		prov.InputChanges = append(prov.InputChanges, "transport")
	} else if !done {
		coord.RecordInputs("transport", stageInputs(rtpFile))
		coord.Begin("transport")
		if err := coord.Commit("transport", stageInputs(rtpFile), digestOf(tstats), nil,
			func() error { return nil }); err != nil {
			return nil, err
		}
	}
	rep.stages = append(rep.stages, "transport")
	if _, err := crashed("transport"); err != nil {
		return rep, err
	}

	// ---- stage: sync ----
	var timelines []clock.TrackTimeline
	for i := range fi.Tracks {
		t := &fi.Tracks[i]
		st := tables[t.TrackID]
		tl := clock.TrackTimeline{TrackID: t.TrackID, Timescale: t.Timescale, Kind: t.Kind}
		for _, s := range st.Samples {
			tl.Samples = append(tl.Samples, s.Timestamp)
		}
		if len(tl.Samples) > 0 {
			tl.Start = tl.Samples[0]
		}
		timelines = append(timelines, tl)
	}
	rtpTL := clock.TrackTimeline{TrackID: 100, Timescale: 90000, Kind: "rtp"}
	for _, p := range pkts {
		ts, err := clock.FromTicks(int64(p.Timestamp), 90000)
		if err != nil {
			return nil, err
		}
		rtpTL.Samples = append(rtpTL.Samples, ts)
	}
	if len(rtpTL.Samples) > 0 {
		rtpTL.Start = rtpTL.Samples[0]
	}
	timelines = append(timelines, rtpTL)
	corr, err := clock.EstimateOffsets(timelines)
	if err != nil {
		return nil, err
	}
	regressions := 0
	for _, c := range corr {
		if c.NonZeroStart {
			prov.AddDecision("sync", "correct-offset",
				fmt.Sprintf("track %d starts at %s s", c.TrackID, c.Offset))
		}
		regressions += len(c.Regressions)
	}
	if regressions > 0 {
		rep.faults = append(rep.faults, faultCount{Stage: "sync", Kind: "timestamp-regression", Count: regressions})
		prov.AddDecision("sync", "mark-regression",
			fmt.Sprintf("%d regressions preserved with original timestamps", regressions))
	}
	skew, err := clock.CheckAVSkew(timelines, corr, clock.SyncConfig{MaxSkew: clock.Must(20, 1000)})
	if err != nil {
		return nil, fmt.Errorf("media: %w", err)
	}
	if done, err := coord.Done("sync", stageInputs(fs.NormalMP4, rtpFile)); err != nil {
		prov.InputChanges = append(prov.InputChanges, "sync")
	} else if !done {
		coord.RecordInputs("sync", stageInputs(fs.NormalMP4, rtpFile))
		coord.Begin("sync")
		if err := coord.Commit("sync", stageInputs(fs.NormalMP4, rtpFile), digestOf(corr), nil,
			func() error { return nil }); err != nil {
			return nil, err
		}
	}
	rep.stages = append(rep.stages, "sync")
	if _, err := crashed("sync"); err != nil {
		return rep, err
	}

	return finishPipeline(&mergeCtx{
		cfg: cfg, workDir: workDir, coord: coord, prov: prov, rep: rep,
		fs: fs, digests: digests, fi: fi, tables: tables,
		rtpFile: rtpFile, tstats: tstats, skew: skew,
		stageInputs: stageInputs, crashed: crashed,
	})
}
