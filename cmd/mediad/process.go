package main

import (
	"errors"
	"os"

	"mediad/internal/audio"
	"mediad/internal/checkpoint"
	"mediad/internal/fixture"
	"mediad/internal/manifest"
	"mediad/internal/pipeline"
)

// cmdProcess runs (or resumes) one normalization job and prints the report.
func cmdProcess(args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	if err := f.require("--in", "--out"); err != nil {
		return err
	}
	inPath := f.get("--in", "")
	outPath := f.get("--out", "")
	rate, err := f.int("--rate", 48000)
	if err != nil {
		return err
	}
	lufs, err := f.float("--target-lufs", -16)
	if err != nil {
		return err
	}
	interrupt, err := f.int("--interrupt-after", 0)
	if err != nil {
		return err
	}
	layout := f.get("--layout", "stereo")
	if audio.ChannelsFor(layout) == 0 {
		return badArgs("unknown layout %q", layout)
	}
	ckptPath := f.get("--checkpoint", "")
	manifestPath := f.get("--manifest", "")

	cfg := pipeline.Config{
		InputPath:    inPath,
		SourceRate:   int(fixture.Default().Rate),
		SourceChans:  fixture.Default().Channels,
		TargetRate:   rate,
		TargetLayout: layout,
		TargetLUFS:   lufs,
		WindowSize:   64,
		BurstRun:     3,
		BlockFrames:  480,
	}

	// Resume only when a checkpoint already exists and no interrupt is asked.
	if ckptPath != "" && interrupt == 0 {
		if _, lerr := checkpoint.Load(ckptPath); lerr == nil {
			return resumeJob(cfg, inPath, outPath, ckptPath, manifestPath)
		}
	}
	return freshJob(cfg, inPath, outPath, ckptPath, interrupt, manifestPath)
}

func freshJob(cfg pipeline.Config, inPath, outPath, ckptPath string, interrupt int, manifestPath string) error {
	in, err := os.Open(inPath)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer out.Close()

	runner, err := pipeline.NewRunner(cfg, out, ckptPath, interrupt)
	if err != nil {
		return err
	}
	rep, err := runner.Run(in)
	if err != nil && !errors.Is(err, pipeline.ErrInterrupted) {
		return err
	}
	printReport(rep)
	if errors.Is(err, pipeline.ErrInterrupted) {
		outf("status=INTERRUPTED checkpoint=%s", ckptPath)
		return nil
	}
	return writeManifest(manifestPath, cfg, rep, outPath)
}

func resumeJob(cfg pipeline.Config, inPath, outPath, ckptPath, manifestPath string) error {
	out, err := os.OpenFile(outPath, os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	runner, suffix, offset, err := pipeline.Resume(cfg, inPath, ckptPath, out)
	if err != nil {
		return err
	}
	rep, err := runner.Run(suffix)
	if err != nil {
		return err
	}
	printReport(rep)
	outf("resume_ms=%d resumed_from_offset=%d", rep.ResumeMS, offset)
	return writeManifest(manifestPath, cfg, rep, outPath)
}

// writeManifest optionally packages delivery metadata into an MP4 container.
func writeManifest(path string, cfg pipeline.Config, rep *pipeline.Report, pcmPath string) error {
	if path == "" {
		return nil
	}
	payload, err := os.ReadFile(pcmPath)
	if err != nil {
		return err
	}
	mf, err := os.Create(path)
	if err != nil {
		return err
	}
	defer mf.Close()
	return manifest.WriteBoxes(mf, manifest.Metadata{
		JobID: cfg.InputPath, SourceRate: cfg.SourceRate,
		TargetRate: cfg.TargetRate, Layout: cfg.TargetLayout,
		TargetLUFS: cfg.TargetLUFS, Packets: rep.Packets, Gaps: rep.Gaps,
		SamplesIn: rep.SamplesIn, SamplesOut: rep.SamplesOut, SHA256: rep.SHA256,
	}, payload)
}

func printReport(r *pipeline.Report) {
	outf("packets=%d duplicates=%d reordered=%d gaps=%d samples_in=%d samples_out=%d peak_dbfs=%.1f clipped=%d sha256=%s",
		r.Packets, r.Duplicates, r.Reordered, r.Gaps,
		r.SamplesIn, r.SamplesOut, r.PeakDBFS, r.Clipped, r.SHA256)
}
