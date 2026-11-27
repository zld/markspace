// Package pipeline wires the scanning, RTP recovery, synchronisation, quality,
// manifest and archive stages into one reproducible run.
package pipeline

import (
	"os"
	"path/filepath"

	"mediacore/internal/fault"
	"mediacore/internal/media"
)

func Run(opts Options) (*Report, error) {
	if opts.Dir == "" {
		return nil, fault.Newf(fault.CodeInternal, "", "demo directory is required")
	}
	for _, sub := range []string{"media", "archive", "manifest"} {
		if err := os.MkdirAll(filepath.Join(opts.Dir, sub), 0o755); err != nil {
			return nil, fault.Newf(fault.CodeInternal, "", "cannot create %s: %v", sub, err)
		}
	}
	rep := &Report{
		Codec: string(opts.Codec), Seed: opts.Seed,
		QualityMin: opts.QualityMin, BudgetMS: opts.BudgetMS,
		TargetKbps: opts.TargetKbps, CorruptIndex: opts.CorruptIndex,
		RecoveredExpected: opts.Loss,
	}
	tool, err := media.Locate()
	if err != nil {
		return rep, err
	}
	syn := media.Synthetic{Width: opts.Width, Height: opts.Height, FPS: opts.FPS, Seconds: opts.Seconds, Seed: opts.Seed}
	mediaDir := filepath.Join(opts.Dir, "media")
	ref, wav, err := tool.Generate(mediaDir, syn)
	if err != nil {
		return rep, err
	}
	encoded := filepath.Join(mediaDir, "encoded."+string(opts.Codec))
	if err := stageQuality(tool, rep, opts, syn, ref, encoded); err != nil {
		return rep, err
	}
	rawBytes, stream, err := stageScan(rep, opts, mediaDir, encoded)
	if err != nil {
		return rep, err
	}
	recoveredBytes, err := stageRecover(rep, opts, rawBytes, stream)
	if err != nil {
		return rep, err
	}
	if err := stageSync(rep, stream, syn); err != nil {
		return rep, err
	}
	if err := stageMigrate(rep, opts, stream, syn); err != nil {
		return rep, err
	}
	if err := stageArchive(tool, rep, opts, syn, encoded, wav, rawBytes, recoveredBytes); err != nil {
		return rep, err
	}
	rep.computeDigest()
	return rep, nil
}
