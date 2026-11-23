package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zld/media-check/media"
	"github.com/zld/media-check/pipeline"
	"github.com/zld/media-check/transport"
)

func demo(ctx context.Context, d *demoFlags) (*demoReport, error) {
	absOut, err := filepath.Abs(d.out)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absOut, 0o755); err != nil {
		return nil, fmt.Errorf("create out dir: %w", err)
	}

	fx, err := buildDemoFixture(d.seed)
	if err != nil {
		return nil, err
	}
	srcPath := filepath.Join(absOut, "source.mov")
	if err := os.WriteFile(srcPath, fx.Bytes, 0o644); err != nil {
		return nil, fmt.Errorf("write source: %w", err)
	}

	// 1. RTP receive drill: duplicates, reorder, burst loss beyond the window.
	drill, err := transport.DefaultRTPDrill(d.seed)
	if err != nil {
		return nil, err
	}
	stats, gaps, ordered, err := drill.Replay(d.window, d.dedupWindow, 0x4d454449)
	if err != nil {
		return nil, err
	}
	for i := 1; i < len(ordered); i++ {
		if ordered[i].ExtSeq <= ordered[i-1].ExtSeq {
			return nil, media.NewError(media.ClassIntegrity, "rtp.order", 0, "released packets are not strictly increasing")
		}
	}

	// 2. Bandwidth adaptation across a changing rate.
	// The controller may only narrow the operator's limits; it never raises
	// concurrency or queue capacity beyond what was requested on the CLI.
	ctl, err := transport.NewController(transport.AdaptOptions{
		Window: 5, MinParallel: 1, MaxParallel: d.workers,
		MinQueue: 1, MaxQueue: d.queue, TargetBps: 300_000,
		SampleEvery: time.Millisecond,
	})
	if err != nil {
		return nil, err
	}
	// Drive the controller with the actual received datagrams at a changing
	// pace so the pool limits are derived from this run's receive pressure.
	ctl.Reset()
	drill.FeedController(ctl, []time.Duration{
		400 * time.Millisecond, 200 * time.Millisecond, 100 * time.Millisecond,
		60 * time.Millisecond, 100 * time.Millisecond, 160 * time.Millisecond,
	})
	minBps, meanBps, maxBps := ctl.Summary()

	// 3. Probe + map + remux the source. A single probe and single file handle
	// are reused so the verification compares exactly what was written.
	probe, _, err := pipeline.Reprobe(srcPath)
	if err != nil {
		return nil, err
	}
	if err := media.ValidateMovieBounds(probe); err != nil {
		return nil, err
	}
	tmap, err := media.BuildTrackMap(probe, demoTargets())
	if err != nil {
		return nil, err
	}
	remuxPath := filepath.Join(absOut, "remux.mp4")
	srcFile, err := os.Open(srcPath)
	if err != nil {
		return nil, err
	}
	remuxRes, err := media.Remux(srcFile, probe, tmap, remuxPath, media.RemuxOptions{OutDir: absOut})
	srcFile.Close()
	if err != nil {
		return nil, err
	}
	roundtrip := 0.0
	matched := 0
	for _, ev := range remuxRes.Tracks {
		if ev.ByteMatch && ev.DTSMatch && ev.CTSMatch && ev.SyncMatch {
			matched++
		}
	}
	if n := len(remuxRes.Tracks); n > 0 {
		roundtrip = float64(matched) / float64(n)
	}

	// 4. Pipeline run: bounded pool, deterministic merge, resume.
	runCtx := ctx
	cancel := func() {}
	if d.cancelAfter > 0 {
		runCtx, cancel = context.WithTimeout(ctx, d.cancelAfter)
	}
	defer cancel()
	_ = runCtx
	report, err := pipeline.RunFused(runCtx, pipeline.RunOptions{
		Source: srcPath, OutDir: absOut, Workers: d.workers, Queue: d.queue,
		SamplesPer: d.samplesPer, Targets: demoTargets(), Seed: d.seed, Resume: true,
	}, pipeline.FusionOptions{RTP: drill, MaxGaps: 64, Bandwidth: ctl})
	if err != nil && ctx.Err() == nil && !errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	if report == nil || report.Map == nil {
		return nil, media.NewError(media.ClassCancelled, "demo", 0, "pipeline produced no manifest")
	}

	// 5. Second run must reproduce the manifest digest exactly.
	report2, err2 := pipeline.RunFused(context.Background(), pipeline.RunOptions{
		Source: srcPath, OutDir: absOut, Workers: d.workers, Queue: d.queue,
		SamplesPer: d.samplesPer, Targets: demoTargets(), Seed: d.seed, Resume: true,
	}, pipeline.FusionOptions{RTP: drill, MaxGaps: 64, Bandwidth: ctl})
	repeatMatch := false
	var man2 *pipeline.Manifest
	if err2 == nil && report2 != nil {
		man2 = report2.Map
		repeatMatch = man2 != nil && man2.SHA256 == report.Map.SHA256
	}

	// 6. Keyframe index and random-read verification against the remuxed file.
	kfHit := 0.0
	remuxProbe, _, err := pipeline.Reprobe(remuxPath)
	if err != nil {
		return nil, err
	}
	remuxTracks := targetOrderedTracks(remuxProbe, remuxRes)
	segs, err := media.SplitSegments(remuxTracks, d.samplesPer)
	if err != nil {
		return nil, err
	}
	rf, err := os.Open(remuxPath)
	if err != nil {
		return nil, err
	}
	defer rf.Close()
	idx, err := media.BuildIndex(remuxTracks, segs)
	if err != nil {
		return nil, err
	}
	if kfHit, _, err = idx.VerifyRandom(rf, remuxTracks, d.seed, 64); err != nil {
		return nil, err
	}

	srcSHA, err := hashPath(srcPath)
	if err != nil {
		return nil, err
	}
	outSHA, err := hashPath(remuxPath)
	if err != nil {
		return nil, err
	}

	segCount := 0
	okSegs := 0
	man := report.Map
	for _, e := range man.Entries {
		segCount++
		if e.Status == pipeline.StatusOK && e.SHA256 != "" {
			okSegs++
		}
	}
	segMatch := 0.0
	if segCount > 0 {
		segMatch = float64(okSegs) / float64(segCount)
	}
	if d.cancelAfter > 0 {
		segMatch = 0
		if n := len(man.Entries); n > 0 {
			segMatch = float64(okSegs) / float64(n)
		}
	}

	// 7. Deterministic mid-processing cancellation drill: exactly four queued
	// tasks are abandoned, all goroutines are reclaimed, and the manifest
	// records them without touching earlier successes.
	cancelledTasks, err := runCancelDrill(absOut)
	if err != nil {
		return nil, err
	}
	stats = mergeRuntimeStats(stats, man)
	return &demoReport{
		RoundtripParse:      roundtrip,
		KeyframeHit:         kfHit,
		SegmentMatch:        segMatch,
		Tracks:              len(probe.Tracks),
		Segments:            len(segs),
		Duplicates:          stats.Duplicates,
		Reordered:           stats.Reordered,
		Lost:                stats.Lost,
		CancelledTasks:      cancelledTasks,
		RepeatManifestMatch: repeatMatch,
		SourcePath:          srcPath,
		OutputPath:          remuxPath,
		ManifestPath:        report.ManifestPath,
		ManifestSHA:         man.SHA256,
		SourceSHA:           srcSHA,
		OutputSHA:           outSHA,
		GapRecords:          gaps,
		MinBps:              minBps, MeanBps: meanBps, MaxBps: maxBps,
		FinalParallel: ctl.Parallel(),
		FinalQueue:    ctl.Queue(),
		PeakQueue:     man.Stats.PeakQueue,
	}, nil
}
