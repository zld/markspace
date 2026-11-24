package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"archive/internal/clock"
)

func runDemo(t *testing.T, dir string) *Summary {
	t.Helper()
	opts := DefaultOptions()
	opts.OutDir = dir
	opts.Generate = true
	res, err := Run(opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Summary == nil {
		t.Fatal("nil summary")
	}
	return res.Summary
}

func TestDemoMeetsAcceptanceCriteria(t *testing.T) {
	sum := runDemo(t, t.TempDir())
	if err := VerifyAcceptance(sum); err != nil {
		t.Fatalf("acceptance: %v", err)
	}
	if sum.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0 when recovery succeeds", sum.ExitCode)
	}
	if len(sum.Normal.RecoveredLayers) != 3 {
		t.Fatalf("normal layers = %v", sum.Normal.RecoveredLayers)
	}
	if sum.Normal.Duplicates == 0 || sum.Normal.Lost != 0 {
		t.Fatalf("normal dups=%d lost=%d", sum.Normal.Duplicates, sum.Normal.Lost)
	}
	if sum.Burst.Lost == 0 || len(sum.Burst.IsolatedLayers) == 0 {
		t.Fatalf("burst lost=%d isolated=%v", sum.Burst.Lost, sum.Burst.IsolatedLayers)
	}
	if sum.Burst.ActiveLayers < 2 {
		t.Fatalf("burst active = %d, want >= 2", sum.Burst.ActiveLayers)
	}
	if sum.BoundaryErrorNS > 1 {
		t.Fatalf("boundary error = %d ns", sum.BoundaryErrorNS)
	}
	if sum.InvalidMetadata.ExitCode == 0 || sum.InvalidMetadata.Kind == "" {
		t.Fatalf("invalid metadata sample not classified: %+v", sum.InvalidMetadata)
	}
	if sum.FrameSHA256 == "" || sum.ManifestSHA256 == "" || sum.FrameSHA256 == sum.ManifestSHA256 {
		t.Fatalf("hash reporting incomplete: frame=%q manifest=%q", sum.FrameSHA256, sum.ManifestSHA256)
	}
}

func TestDemoRepeatRunsAreDeterministic(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	a := runDemo(t, dirA)
	b := runDemo(t, dirB)
	if a.FrameSHA256 != b.FrameSHA256 {
		t.Fatalf("frame hash differs:\n%s\n%s", a.FrameSHA256, b.FrameSHA256)
	}
	if a.ManifestSHA256 != b.ManifestSHA256 {
		t.Fatalf("manifest hash differs:\n%s\n%s", a.ManifestSHA256, b.ManifestSHA256)
	}
	ma, err := os.ReadFile(filepath.Join(dirA, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	mb, err := os.ReadFile(filepath.Join(dirB, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(ma) != string(mb) {
		t.Fatal("manifest.json bytes differ between identical runs")
	}
}

func TestCorruptSampleReportsLocationAndNonZeroCode(t *testing.T) {
	sum := runDemo(t, t.TempDir())
	if sum.Corrupt.ExitCode == 0 {
		t.Fatal("corrupt sample exit code must be non-zero")
	}
	if sum.Corrupt.Box == "" || sum.Corrupt.DeclaredEnd <= sum.Corrupt.FileSize {
		t.Fatalf("corrupt info lacks locatable overrun: %+v", sum.Corrupt)
	}
	if clock.EnsureErrorKind(nil, clock.KindTruncated) != "" {
		t.Fatal("unexpected non-empty kind")
	}
	// Simulate the CLI path: a corruption-only failure keeps exit code 3.
	if got := ClassifyExit(clock.New(clock.KindTruncated, "x")); got != 3 {
		t.Fatalf("truncated exit = %d", got)
	}
}

func TestSummaryJSONRoundTrips(t *testing.T) {
	sum := runDemo(t, t.TempDir())
	b, err := json.Marshal(sum)
	if err != nil {
		t.Fatal(err)
	}
	var back Summary
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.FrameSHA256 != sum.FrameSHA256 {
		t.Fatal("hash lost through JSON")
	}
}
