package verify

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"streamcheck/internal/fixture"
	"streamcheck/internal/manifest"
	"streamcheck/internal/model"
	"streamcheck/internal/scan"
)

func generate(t *testing.T, plan fixture.Plan) string {
	t.Helper()
	if err := fixture.Generate(context.Background(), plan); err != nil {
		t.Fatalf("generate: %v", err)
	}
	return plan.Root
}

func runVerify(t *testing.T, root string, opts Options) Result {
	t.Helper()
	scanned, err := scan.Scan(context.Background(), scan.Options{Root: root, Rates: opts.Rates})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	res, err := Run(context.Background(), root, scanned, opts)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	return res
}

func TestCleanFixturePasses(t *testing.T) {
	root := generate(t, fixture.Plan{Root: t.TempDir(), Rates: []int{800000, 1600000}, Segments: 12})
	res := runVerify(t, root, Options{})
	if res.Failed() {
		t.Fatalf("clean fixture failed: %+v", res.Issues)
	}
	if res.MissingCount != 0 || res.OutOfOrderCount != 0 || res.ChecksumFailures != 0 {
		t.Fatalf("unexpected counts: %+v", res)
	}
	if len(res.Rates) != 2 {
		t.Fatalf("rate count = %d", len(res.Rates))
	}
}

func TestMissingSegmentRespectsTolerance(t *testing.T) {
	// Drop the same number from every rate so that no cross-rate drift is
	// introduced and the missing count is the only finding.
	root := generate(t, fixture.Plan{
		Root:     t.TempDir(),
		Rates:    []int{800000, 1600000},
		Segments: 12,
		Drop:     map[int]int{800000: 5, 1600000: 5},
	})
	strict := runVerify(t, root, Options{})
	if strict.MissingCount != 2 {
		t.Fatalf("missing_count = %d, want 2", strict.MissingCount)
	}
	if strict.Rates[0].MissingCount != 1 || strict.Rates[1].MissingCount != 1 {
		t.Fatalf("per-rate missing = %d/%d", strict.Rates[0].MissingCount, strict.Rates[1].MissingCount)
	}
	if !strict.Failed() {
		t.Fatal("expected failure with zero tolerance")
	}

	tolerant := runVerify(t, root, Options{MissingTolerance: 2})
	if tolerant.Failed() {
		t.Fatalf("tolerance 2 should pass missing-only findings: %+v", tolerant.Issues)
	}
	for _, issue := range tolerant.Issues {
		if issue.Kind != model.IssueMissing {
			t.Fatalf("unexpected non-missing issue under tolerance: %+v", issue)
		}
		if issue.Number != 5 {
			t.Fatalf("unexpected missing number: %+v", issue)
		}
	}
}

func TestToleranceDoesNotHideOutOfOrder(t *testing.T) {
	root := generate(t, fixture.Plan{
		Root:     t.TempDir(),
		Rates:    []int{800000, 1600000},
		Segments: 12,
		Drop:     map[int]int{800000: 5, 1600000: 5},
		Shuffle:  map[int]bool{1600000: true},
	})
	res := runVerify(t, root, Options{MissingTolerance: 2})
	if !res.Failed() {
		t.Fatal("out-of-order findings must fail regardless of missing tolerance")
	}
}

func TestShuffleReportsOutOfOrder(t *testing.T) {
	root := generate(t, fixture.Plan{
		Root:     t.TempDir(),
		Rates:    []int{800000, 3200000},
		Segments: 12,
		Shuffle:  map[int]bool{3200000: true},
	})
	res := runVerify(t, root, Options{})
	if res.OutOfOrderCount == 0 {
		t.Fatalf("out_of_order_count = 0, issues: %+v", res.Issues)
	}
	if !res.Failed() {
		t.Fatal("expected failure")
	}
	if res.Rates[1].OutOfOrderCount == 0 {
		t.Fatalf("per-rate out_of_order = %d", res.Rates[1].OutOfOrderCount)
	}
}

func TestCrossRateDeviationReported(t *testing.T) {
	root := t.TempDir()
	// Both rates declare four segments, but 1600000's second segment runs
	// 200ms longer. Nothing is missing, so only a boundary deviation can
	// explain the difference.
	durations := map[int][]int64{
		800000:  {1000, 1000, 1000, 1000},
		1600000: {1000, 1200, 1000, 1000},
	}
	for rate, list := range durations {
		var segments []model.Segment
		for number, duration := range list {
			name := fmt.Sprintf("seg_%d_%d_%d.ts", rate, number, duration)
			if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o644); err != nil {
				t.Fatal(err)
			}
			segments = append(segments, model.Segment{Rate: rate, Number: number, DurationMS: duration, Path: name})
		}
		data, err := manifest.Build(rate, segments)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("%d.m3u8", rate)), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res := runVerify(t, root, Options{DeviationThresholdMS: 100})
	if res.MaxDeviationMS != 200 {
		t.Fatalf("max deviation = %d, want 200; issues: %+v", res.MaxDeviationMS, res.Issues)
	}
	var found bool
	for _, issue := range res.Issues {
		if issue.Kind == model.IssueCrossRate {
			found = true
		}
	}
	if !found {
		t.Fatalf("cross-rate issue not reported: %+v", res.Issues)
	}
}

func TestManifestDisagreementReported(t *testing.T) {
	root := generate(t, fixture.Plan{Root: t.TempDir(), Rates: []int{800000}, Segments: 3})
	path := filepath.Join(root, "800000.m3u8")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Drop one declared entry so the manifest and disk disagree both ways.
	trimmed := strings.Replace(string(body), "#EXTINF:1.000,\nseg_800000_1_1000.ts\n", "", 1)
	if err := os.WriteFile(path, []byte(trimmed), 0o644); err != nil {
		t.Fatal(err)
	}
	res := runVerify(t, root, Options{})
	if res.Rates[0].HasManifest != true {
		t.Fatal("manifest should parse")
	}
	// A file on disk that no entry declares is an undeclared-file finding.
	if !hasKind(res.Issues, model.IssueManifest) {
		t.Fatalf("expected undeclared-file finding, got %+v", res.Issues)
	}
	if hasKind(res.Issues, model.IssueMissing) {
		t.Fatalf("nothing is undeclared on the manifest side, got %+v", res.Issues)
	}
}

func TestDeclaredButAbsentFileReported(t *testing.T) {
	root := generate(t, fixture.Plan{Root: t.TempDir(), Rates: []int{800000}, Segments: 3})
	if err := os.Remove(filepath.Join(root, "seg_800000_1_1000.ts")); err != nil {
		t.Fatal(err)
	}
	res := runVerify(t, root, Options{})
	if !hasKind(res.Issues, model.IssueMissing) {
		t.Fatalf("expected missing finding, got %+v", res.Issues)
	}
	if res.MissingCount != 1 {
		t.Fatalf("missing_count = %d, want 1", res.MissingCount)
	}
}

func hasKind(issues []model.Issue, kind model.IssueKind) bool {
	for _, issue := range issues {
		if issue.Kind == kind {
			return true
		}
	}
	return false
}
