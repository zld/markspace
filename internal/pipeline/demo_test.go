package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediapack/internal/fault"
	"mediapack/internal/recovery"
	"mediapack/internal/report"
)

func TestDemoDeterministicReuseAndStableManifest(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	first, err := RunDemo(ctx, Options{Out: dir})
	if err != nil {
		t.Fatal(err)
	}
	if first.RebuiltAssets == 0 || first.ReusedAssets != 0 {
		t.Fatalf("first plan reused=%d rebuilt=%d", first.ReusedAssets, first.RebuiltAssets)
	}
	if !first.OK || !first.Published {
		t.Fatalf("first report ok=%v published=%v", first.OK, first.Published)
	}

	second, err := RunDemo(ctx, Options{Out: dir})
	if err != nil {
		t.Fatal(err)
	}
	if second.RebuiltAssets != 0 || second.ReusedAssets == 0 {
		t.Fatalf("second plan reused=%d rebuilt=%d", second.ReusedAssets, second.RebuiltAssets)
	}
	if second.SegmentSHA256 != first.SegmentSHA256 {
		t.Fatal("segment aggregate changed on deterministic rerun")
	}
	if !equalStringSlices(second.SegmentHashes, first.SegmentHashes) {
		t.Fatal("ordered segment hashes changed")
	}

	third, err := RunDemo(ctx, Options{Out: dir})
	if err != nil {
		t.Fatal(err)
	}
	if third.ManifestSHA256 != second.ManifestSHA256 {
		t.Fatalf("no-op manifests differ: %s vs %s", third.ManifestSHA256, second.ManifestSHA256)
	}
	if third.RebuiltAssets != 0 {
		t.Fatalf("third rebuilt=%d", third.RebuiltAssets)
	}
}

func TestDemoCorruptBoundaryThenResumePreservesHealthy(t *testing.T) {
	ctx := context.Background()
	cleanDir := t.TempDir()
	clean, err := RunDemo(ctx, Options{Out: cleanDir})
	if err != nil {
		t.Fatal(err)
	}

	badDir := t.TempDir()
	bad, err := RunDemo(ctx, Options{Out: badDir, CorruptBoundary: true})
	if fault.Classify(err) != fault.BoundaryMismatch {
		t.Fatalf("corrupt err kind=%q err=%v", fault.Classify(err), err)
	}
	var fe *fault.Error
	if !errors.As(err, &fe) || fe.Layer != CorruptLayer || fe.Segment == nil || *fe.Segment != 2 {
		t.Fatalf("error location=%+v", fe)
	}
	if bad.Failure == nil || bad.Failure.OffsetMS != 120 {
		t.Fatalf("failure report=%+v", bad.Failure)
	}
	badHashes := layerHashMap(bad)

	resumed, err := RunDemo(ctx, Options{Out: badDir, Resume: true})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !resumed.OK || resumed.RebuiltAssets != 1 {
		t.Fatalf("resume ok=%v rebuilt=%d failure=%+v", resumed.OK, resumed.RebuiltAssets, resumed.Failure)
	}
	newHashes := layerHashMap(resumed)
	for layer, old := range badHashes {
		if layer == CorruptLayer {
			if len(newHashes[layer]) != 3 || newHashes[layer][0] != old[0] {
				t.Fatalf("corrupt layer did not preserve segment 1: old=%v new=%v", old, newHashes[layer])
			}
			continue
		}
		if !equalStringSlices(newHashes[layer], old) {
			t.Fatalf("healthy layer %s changed: old=%v new=%v", layer, old, newHashes[layer])
		}
	}
	if resumed.SegmentSHA256 != clean.SegmentSHA256 {
		t.Fatalf("recovered aggregate %s != clean %s", resumed.SegmentSHA256, clean.SegmentSHA256)
	}
}

func TestCorruptRunDoesNotReplacePublishedManifest(t *testing.T) {
	dir := t.TempDir()
	old := []byte(`{"version":"previous-good"}`)
	if _, err := recovery.Publish(dir, old); err != nil {
		t.Fatal(err)
	}
	_, err := RunDemo(context.Background(), Options{Out: dir, CorruptBoundary: true})
	if fault.Classify(err) != fault.BoundaryMismatch {
		t.Fatalf("err=%v", err)
	}
	current, err := os.ReadFile(filepath.Join(dir, "published", "current.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != string(old) {
		t.Fatalf("published manifest was replaced:\n%s", current)
	}
}

func layerHashMap(r *report.Report) map[string][]string {
	out := make(map[string][]string, len(r.Layers))
	for _, layer := range r.Layers {
		hashes := make([]string, len(layer.SegmentHashes))
		for i, sh := range layer.SegmentHashes {
			hashes[i] = sh.SHA256
		}
		out[layer.Name] = hashes
	}
	return out
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
