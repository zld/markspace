package kernel

import (
	"os"
	"path/filepath"
	"testing"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	cfg := DefaultConfig()
	cfg.WorkDir = t.TempDir()
	return cfg
}

func TestDemoMeetsAcceptance(t *testing.T) {
	cfg := testConfig(t)
	res, err := Demo(cfg)
	if err != nil {
		t.Fatalf("Demo: %v", err)
	}
	if !res.OK {
		t.Fatalf("acceptance not met: failure=%q code=%d metrics=%+v corrupt=%+v",
			res.Failure, res.FailureCode, res.Metrics, res.Corrupt)
	}
	if res.Metrics.RoundtripConsistency < 0.9999 {
		t.Fatalf("roundtrip = %f", res.Metrics.RoundtripConsistency)
	}
	if res.Metrics.KeyframeHitRate < 0.99 {
		t.Fatalf("keyframe hit rate = %f", res.Metrics.KeyframeHitRate)
	}
	if res.Metrics.Segments < 12 {
		t.Fatalf("segments = %d", res.Metrics.Segments)
	}
	if res.SegmentHashesVerified != res.Metrics.Segments {
		t.Fatalf("hashes verified = %d, segments = %d", res.SegmentHashesVerified, res.Metrics.Segments)
	}
	if res.Metrics.Recomputed > 1 {
		t.Fatalf("resume recomputed wasted work = %d", res.Metrics.Recomputed)
	}
	if !res.ProvenanceVerified {
		t.Fatal("provenance not verified")
	}
	if res.Elements < 500 {
		t.Fatalf("elements = %d, want >=500", res.Elements)
	}
}

func TestDemoCorruptionSuite(t *testing.T) {
	cfg := testConfig(t)
	res, err := Demo(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Corrupt.Cases != 5 {
		t.Fatalf("cases = %d", res.Corrupt.Cases)
	}
	if res.Corrupt.Rejected != 5 {
		t.Fatalf("rejected = %d, details=%v", res.Corrupt.Rejected, res.Corrupt.Details)
	}
	if res.Corrupt.PartialOutputs != 0 {
		t.Fatalf("partial outputs = %d", res.Corrupt.PartialOutputs)
	}
}

func TestDemoDeterministic(t *testing.T) {
	a := testConfig(t)
	b := testConfig(t)
	ra, err := Demo(a)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := Demo(b)
	if err != nil {
		t.Fatal(err)
	}
	ma, _ := os.ReadFile(ra.ManifestPath)
	mb, _ := os.ReadFile(rb.ManifestPath)
	if string(ma) != string(mb) {
		t.Fatal("manifest bytes differ between runs with identical seed")
	}
	if ra.Metrics != rb.Metrics {
		t.Fatalf("metrics differ: %+v != %+v", ra.Metrics, rb.Metrics)
	}
}

func TestStandaloneVerify(t *testing.T) {
	cfg := testConfig(t)
	res, err := Demo(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyFiles(res.ManifestPath, res.InputPath, res.ProvenancePath); err != nil {
		t.Fatalf("VerifyFiles: %v", err)
	}
	// Tamper with the input and expect a provenance failure.
	data, err := os.ReadFile(res.InputPath)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 0xFF
	tampered := filepath.Join(t.TempDir(), "tampered.webm")
	if err := os.WriteFile(tampered, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyFiles(res.ManifestPath, tampered, res.ProvenancePath); err == nil {
		t.Fatal("tampered input verified")
	} else if CodeOf(err) != ExitProvenance {
		t.Fatalf("exit code = %d, want provenance", CodeOf(err))
	}
}

func TestVerifyRejectsTamperedManifest(t *testing.T) {
	cfg := testConfig(t)
	res, err := Demo(cfg)
	if err != nil {
		t.Fatal(err)
	}
	m, err := segmentLoad(res.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	m.Segments[0].Hash = "00" + m.Segments[0].Hash[2:]
	if err := writeManifest(res.ManifestPath, m); err != nil {
		t.Fatal(err)
	}
	if err := VerifyFiles(res.ManifestPath, res.InputPath, res.ProvenancePath); err == nil {
		t.Fatal("tampered manifest verified")
	}
}
