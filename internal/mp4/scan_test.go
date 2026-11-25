package mp4scan_test

import (
	"path/filepath"
	"testing"

	"archivecheck/internal/clock"
	apperrors "archivecheck/internal/errors"
	"archivecheck/internal/fixture"
	"archivecheck/internal/model"
	mp4scan "archivecheck/internal/mp4"
)

func resolvedAsset(t *testing.T, dir, manifestPath string, assetIndex int) model.InputAsset {
	t.Helper()
	m, err := fixture.LoadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	a := m.Assets[assetIndex]
	a.Path = filepath.Join(dir, a.Path)
	return a
}

func TestScanValidTimelineAndKeyframeRecovery(t *testing.T) {
	dir := t.TempDir()
	paths, err := fixture.Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := resolvedAsset(t, dir, paths.ValidInput, 0)
	got, err := mp4scan.ScanAsset(a)
	if err != nil {
		t.Fatalf("ScanAsset valid: %v", err)
	}
	if got.Timeline.ContainerStart != clock.Tick(fixture.StartTimestamp) {
		t.Fatalf("container start=%d want %d", got.Timeline.ContainerStart, fixture.StartTimestamp)
	}
	if got.Timeline.EditZeroPoint != clock.Tick(fixture.StartTimestamp) {
		t.Fatalf("zero point=%d want %d", got.Timeline.EditZeroPoint, fixture.StartTimestamp)
	}
	if !got.Timeline.NonZeroStartPreserved {
		t.Fatal("non-zero start was not preserved")
	}
	if len(got.Keyframes) != 3 {
		t.Fatalf("keyframes=%v want 3", got.Keyframes)
	}
	rec, err := got.RecoverKeyframe(fixture.TargetBoundary)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Index != fixture.RecoveredAnchor || rec.PTS != 30_000 {
		t.Fatalf("recovery=(%d,%d) want sample 5 at 30000", rec.Index, rec.PTS)
	}
}

func TestScanRejectsBadContainersAndEdits(t *testing.T) {
	dir := t.TempDir()
	paths, err := fixture.Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		manifest string
		kind     apperrors.Kind
		index    int
	}{
		{"truncated", paths.Truncated, apperrors.KindTruncated, -1},
		{"corrupt box", paths.CorruptBox, apperrors.KindContainer, -1},
		{"no keyframe", paths.NoKeyframe, apperrors.KindNoKeyframe, -1},
		{"empty edit", filepath.Join(dir, "bad-edit-empty.json"), apperrors.KindEditList, 0},
		{"oob edit", filepath.Join(dir, "bad-edit-oob.json"), apperrors.KindEditList, 1},
		{"reverse edit", filepath.Join(dir, "bad-edit-reverse.json"), apperrors.KindEditList, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := resolvedAsset(t, dir, tt.manifest, 0)
			_, err := mp4scan.ScanAsset(a)
			if err == nil {
				t.Fatal("expected scan failure")
			}
			if k := apperrors.KindOf(err); k != tt.kind {
				t.Fatalf("kind=%q want %q err=%v", k, tt.kind, err)
			}
			if tt.index >= 0 {
				var ce *apperrors.Error
				if !asError(err, &ce) || ce.IntervalIndex == nil || *ce.IntervalIndex != tt.index {
					t.Fatalf("interval index error: %v", err)
				}
			}
		})
	}
}

func asError(err error, target **apperrors.Error) bool {
	for err != nil {
		if e, ok := err.(*apperrors.Error); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
