package demo

import (
	"context"
	"fmt"

	"mediarc/container"
	"mediarc/model"
	"mediarc/sample"
)

func (r *runner) runContainers(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return model.New(model.CodeCanceled, "demo", "canceled before containers").WithCause(err)
	}
	build := func(name string, opts sample.MP4Options) (sample.MP4Fixture, error) {
		fixture, err := sample.BuildMP4(r.fixtureDir, name, opts)
		if err != nil {
			return sample.MP4Fixture{}, model.Fatal(model.CodeInvalidArgument, "demo",
				"cannot build MP4 fixture "+name).WithCause(err)
		}
		r.addFixture(name)
		return fixture, nil
	}
	good, err := build("healthy.mp4", sample.MP4Options{})
	if err != nil {
		return err
	}
	truncated, err := build("truncated.mp4", sample.MP4Options{TruncateBytes: 9000})
	if err != nil {
		return err
	}
	broken, err := build("broken-index.mp4", sample.MP4Options{IndexShiftWidens: 5_000_000})
	if err != nil {
		return err
	}
	missing, err := build("no-index.mp4", sample.MP4Options{OmitIndex: true})
	if err != nil {
		return err
	}

	healthy, err := container.ScanFile(good.Path, container.ScanOptions{})
	if err != nil {
		return err
	}
	trunc, err := container.ScanFile(truncated.Path, container.ScanOptions{})
	if err != nil {
		return err
	}
	brokenIdx, err := container.ScanFile(broken.Path, container.ScanOptions{})
	if err != nil {
		return err
	}
	noIdx, err := container.ScanFile(missing.Path, container.ScanOptions{})
	if err != nil {
		return err
	}

	checks := []Assertion{
		assert("healthy MP4 index is complete", boolText(healthy.IndexComplete), "true", healthy.IndexComplete),
		assert("healthy MP4 recovers every indexed sample", fmt.Sprint(healthy.IndexedSampleCount()),
			fmt.Sprint(good.IntactSamples), healthy.IndexedSampleCount() == good.IntactSamples),
		assert("truncated MP4 reports a first unreadable offset inside the file",
			fmt.Sprintf("%d in [0,%d]", trunc.FirstUnreadable, trunc.Size), "inside", trunc.RecoveredWithinFile()),
		assert("truncated MP4 keeps the intact indexed samples", fmt.Sprint(trunc.IndexedSampleCount()),
			fmt.Sprint(truncated.ExpectedRecovered), trunc.IndexedSampleCount() == truncated.ExpectedRecovered),
		assert("truncated MP4 never reads past the input", boolText(samplesInside(trunc)), "true", samplesInside(trunc)),
		assert("truncated MP4 recovers the readable mdat tail", fmt.Sprint(trunc.RecoveredSamples), ">0",
			trunc.RecoveredSamples > 0),
		assert("broken-index MP4 falls back to boundary scanning",
			fmt.Sprintf("complete=%t recovered=%d", brokenIdx.IndexComplete, brokenIdx.RecoveredSamples), "complete=false recovered>0",
			!brokenIdx.IndexComplete && brokenIdx.RecoveredSamples > 0),
		assert("broken-index first unreadable offset stays inside the file",
			boolText(brokenIdx.RecoveredWithinFile()), "true", brokenIdx.RecoveredWithinFile()),
		assert("missing-index MP4 recovers mdat content",
			fmt.Sprintf("complete=%t recovered=%d", noIdx.IndexComplete, noIdx.RecoveredSamples), "complete=false recovered>0",
			!noIdx.IndexComplete && noIdx.RecoveredSamples > 0),
		assert("missing-index first unreadable offset stays inside the file",
			boolText(noIdx.RecoveredWithinFile()), "true", noIdx.RecoveredWithinFile()),
	}
	r.metrics.FirstBadOff = trunc.FirstUnreadable
	r.record(Scenario{
		Name: "containers",
		Detail: fmt.Sprintf("healthy %d samples; truncated %d indexed + %d scanned, first unreadable %d/%d; "+
			"broken index %d scanned; no index %d scanned",
			healthy.IndexedSampleCount(), trunc.IndexedSampleCount(), trunc.RecoveredSamples,
			trunc.FirstUnreadable, trunc.Size, brokenIdx.RecoveredSamples, noIdx.RecoveredSamples),
		Checks: checks,
	})
	return nil
}

func samplesInside(result container.ScanResult) bool {
	for _, track := range result.Tracks {
		for _, s := range track.Samples {
			if s.Offset < 0 || s.Offset+int64(s.Size) > result.Size {
				return false
			}
		}
	}
	return true
}

// --------------------------------------------------------------------- RTP

func init() { register("containers", 2, (*runner).runContainers) }
