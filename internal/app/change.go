package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"mediacore/internal/diagnostic"
	"mediacore/internal/fixture"
	"mediacore/internal/manifest"
	"mediacore/internal/pipe"
	"mediacore/internal/provenance"
)

func runChangedInput(ctx context.Context, b *fixture.Bundle, work string, memoryMB int) error {
	src := filepath.Join(b.WorkRoot, "normal")
	dst := filepath.Join(work, "change-input")
	_ = os.RemoveAll(dst)
	if err := copyDir(src, dst); err != nil {
		return err
	}
	manifestPath := filepath.Join(dst, "manifest.mpd")
	runDir := filepath.Join(work, "runs", "change-input")
	_ = os.RemoveAll(runDir)

	d, err := manifest.ParseDASH(manifestPath, work)
	if err != nil {
		return err
	}
	digest, _, err := provenance.CombinedInputDigest(manifestPath, d.Entries)
	if err != nil {
		return err
	}
	cfg := pipe.DefaultConfig(runDir)
	if memoryMB > 0 {
		cfg.MaxBufferBytes = int64(memoryMB) << 20
	}
	if _, err := pipe.ProcessEntries(ctx, d.Entries, digest, cfg); err != nil {
		return fmt.Errorf("initial checkpoint run failed: %w", err)
	}

	target := filepath.Join(dst, "seg-video-1.m4s")
	f, err := os.OpenFile(target, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte("input-change")); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	d2, err := manifest.ParseDASH(manifestPath, work)
	if err != nil {
		return err
	}
	newDigest, _, err := provenance.CombinedInputDigest(manifestPath, d2.Entries)
	if err != nil {
		return err
	}
	if newDigest == digest {
		return diagnostic.New(nil, diagnostic.Location{Stage: "verify"}, "test mutation did not change input digest")
	}
	_, err = pipe.ProcessEntries(ctx, d2.Entries, newDigest, cfg)
	if err == nil {
		return diagnostic.New(diagnostic.ErrCheckpoint, diagnostic.Location{Stage: "checkpoint"}, "stale checkpoint was accepted after input change")
	}
	return err
}

func runChangedInputExpected(ctx context.Context, b *fixture.Bundle, work string, memoryMB int) CheckResult {
	err := runChangedInput(ctx, b, work, memoryMB)
	pos := diagnostic.PositionOf(err)
	code := diagnostic.ExitCode(err)
	return CheckResult{
		Name:         "changed-input-checkpoint",
		OK:           code == diagnostic.ExitCheckpointInput,
		ExpectedExit: diagnostic.ExitCheckpointInput,
		ObservedExit: code,
		Position:     pos,
	}
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
