package demo_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"assetpack/internal/clock"
	"assetpack/internal/demo"
	"assetpack/internal/release"
	"assetpack/internal/render"
)

func TestRunProducesVerifiableDeterministicPackage(t *testing.T) {
	out := filepath.Join(t.TempDir(), "pack")
	opts := demo.Options{
		Out: out, Sequence: 1, QueueBlocks: 2,
		Range: clock.Range{From: 45000, To: 135000},
	}
	res, err := demo.Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.DesktopCount != 24 || res.MobileCount != 12 || res.AudioSamples != 48000 {
		t.Fatalf("counts d=%d m=%d a=%d", res.DesktopCount, res.MobileCount, res.AudioSamples)
	}
	if res.Stats.QueuePeak > 2 {
		t.Fatalf("queue peak %d", res.Stats.QueuePeak)
	}
	if res.Commit.Manifest.Targets[0].AlignmentErrorSamples != 0 {
		t.Fatalf("exact range should align: %+v", res.Commit.Manifest.Targets[0])
	}
	if _, err := release.VerifyWithPixels(filepath.Join(out, release.CurrentLink)); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(out, "desktop", "frame-00012.rgba"))
	if err != nil {
		t.Fatal(err)
	}
	if got := render.HashBytes(b); got != res.Frame12Hash || len(got) != 64 {
		t.Fatalf("frame hash mismatch got=%s result=%s", got, res.Frame12Hash)
	}

	res2, err := demo.Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Frame12Hash != res.Frame12Hash {
		t.Fatal("rerun hash changed")
	}
}

func TestRunRejectsBadMDATAndOutOfRange(t *testing.T) {
	_, err := demo.Run(context.Background(), demo.Options{
		Out: filepath.Join(t.TempDir(), "bad"), Sequence: 2, Fault: "bad-mdat",
		Range: clock.Range{From: 45000, To: 135000},
	})
	if err == nil {
		t.Fatal("bad mdat must fail")
	}

	_, err = demo.Run(context.Background(), demo.Options{
		Out: t.TempDir(), Sequence: 1,
		Range: clock.Range{From: 180000, To: 180001},
	})
	var le *clock.Error
	if !errors.As(err, &le) {
		t.Fatalf("expected located clock error, got %v", err)
	}
}
