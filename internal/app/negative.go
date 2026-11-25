package app

import (
	"context"
	"fmt"
	"path/filepath"

	"mediacore/internal/diagnostic"
	"mediacore/internal/fixture"
	"mediacore/internal/manifest"
	"mediacore/internal/pipe"
	"mediacore/internal/provenance"
)

func executeCase(ctx context.Context, c fixture.ManifestCase, work string, memoryMB int) (error, diagnostic.Position) {
	var err error
	switch c.Kind {
	case "zero-timescale":
		_, err = manifest.ParseDASH(c.Manifest, work)
	case "time-regression":
		_, err = manifest.ParseHLS(c.Manifest, work)
	default:
		d, perr := manifest.ParseDASH(c.Manifest, work)
		if perr != nil {
			err = perr
			break
		}
		digest, _, derr := provenance.CombinedInputDigest(c.Manifest, d.Entries)
		if derr != nil {
			err = derr
			break
		}
		cfg := pipe.DefaultConfig(filepath.Join(work, "negative", c.Name))
		if memoryMB > 0 {
			cfg.MaxBufferBytes = int64(memoryMB) << 20
		}
		_, err = pipe.ProcessEntries(ctx, d.Entries, digest, cfg)
	}
	return err, diagnostic.PositionOf(err)
}

func runExpectedCase(ctx context.Context, c fixture.ManifestCase, work string, memoryMB int) CheckResult {
	err, pos := executeCase(ctx, c, work, memoryMB)
	res := CheckResult{
		Name:         c.Name,
		ExpectedExit: c.ExpectedExitCode,
		ObservedExit: diagnostic.ExitCode(err),
		Position:     pos,
	}
	if err == nil {
		res.Message = "expected error but pipeline succeeded"
		return res
	}
	ok := res.ObservedExit == c.ExpectedExitCode
	if c.Representation != "" && pos.Representation != c.Representation {
		ok = false
		res.Message += fmt.Sprintf(" representation=%q want %q;", pos.Representation, c.Representation)
	}
	if c.EntryIndex >= 0 && pos.EntryIndex != c.EntryIndex {
		ok = false
		res.Message += fmt.Sprintf(" entry_index=%d want %d;", pos.EntryIndex, c.EntryIndex)
	}
	if c.ByteOffset > 0 && pos.ByteOffset != c.ByteOffset {
		ok = false
		res.Message += fmt.Sprintf(" byte_offset=%d want %d;", pos.ByteOffset, c.ByteOffset)
	}
	res.OK = ok
	return res
}

func runSingleCase(ctx context.Context, b *fixture.Bundle, work string, memoryMB int, name string) error {
	if name == "changed-input" || name == "checkpoint-input" {
		return runChangedInput(ctx, b, work, memoryMB)
	}
	for _, c := range b.Cases {
		if c.Name == name || c.Kind == name {
			err, _ := executeCase(ctx, c, work, memoryMB)
			return err
		}
	}
	if name == "misaligned" {
		d, err := manifest.ParseDASH(b.MisalignedDASH, work)
		if err != nil {
			return err
		}
		digest, _, err := provenance.CombinedInputDigest(b.MisalignedDASH, d.Entries)
		if err != nil {
			return err
		}
		cfg := pipe.DefaultConfig(filepath.Join(work, "single-misaligned"))
		rep, err := pipe.ProcessEntries(ctx, d.Entries, digest, cfg)
		if err != nil {
			return err
		}
		for _, o := range rep.Outcomes {
			if o.AlignmentErr != nil {
				return o.AlignmentErr
			}
		}
	}
	return fmt.Errorf("unknown case %q", name)
}
