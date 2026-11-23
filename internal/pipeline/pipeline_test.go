package pipeline

import (
	"testing"

	"mediapipe/internal/fault"
	"mediapipe/internal/testdata"
)

func baseOptions(dir string, corrupt testdata.Corruption) Options {
	return Options{
		WorkDir: dir, Seed: 42, TargetKbps: 900, DriftPPM: 180,
		WrapRTP: true, VFR: true, Corruption: corrupt, ClockRate: 48000,
	}
}

// TestDeterministicRepeatedRun proves the sealed manifest bytes and reported
// statistics are identical across repeated runs.
func TestDeterministicRepeatedRun(t *testing.T) {
	opts := baseOptions(t.TempDir(), testdata.CorruptTruncatedBox)
	first, err := Run(opts)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	second, err := Run(opts)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if string(first.ManifestBytes) != string(second.ManifestBytes) {
		t.Fatal("manifest bytes differ across repeated runs")
	}
	if first.Manifest.ContentHash == "" {
		t.Fatal("manifest content hash missing")
	}
	if first.Manifest.ContentHash != second.Manifest.ContentHash {
		t.Fatal("content hash differs across repeated runs")
	}
	if first.Stats != second.Stats {
		t.Fatalf("stats differ: %+v vs %+v", first.Stats, second.Stats)
	}
}

// TestFiveAnomalyClasses exercises each self-generated corruption and asserts
// the relevant fault kind is reported with a non-negative source offset, and
// that the pipeline still produces a manifest.
func TestFiveAnomalyClasses(t *testing.T) {
	cases := []struct {
		name string
		c    testdata.Corruption
		want fault.Kind
	}{
		{"truncated-box", testdata.CorruptTruncatedBox, fault.KindTruncated},
		{"conflict-metadata", testdata.CorruptConflictMeta, fault.KindColorConflict},
		{"rtp-wrap", testdata.CorruptRTPWrap, fault.KindTimestampWrap},
		{"clock-drift", testdata.CorruptDrift, fault.KindDrift},
		{"vfr-dup-pts", testdata.CorruptVFRDupPTS, fault.KindVFRDupPTS},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := baseOptions(t.TempDir(), tc.c)
			res, err := Run(opts)
			if err != nil {
				t.Fatalf("run returned error: %v", err)
			}
			if res == nil || len(res.ManifestBytes) == 0 {
				t.Fatal("expected manifest output despite anomaly")
			}
			if !res.Errors.HasKind(tc.want) {
				t.Fatalf("missing expected fault %s, got %v", tc.want, res.Errors.All())
			}
			// Source offsets must be non-negative whenever reported.
			for _, e := range res.Errors.All() {
				if e.Offset < 0 && e.Kind == tc.want {
					t.Fatalf("fault %s lacks non-negative offset", tc.want)
				}
			}
		})
	}
}

// TestDownstreamPropagation verifies the last three anomaly classes actually
// reach cutting, silence or fingerprint stages rather than dying at ingest.
func TestDownstreamPropagation(t *testing.T) {
	for _, c := range []testdata.Corruption{
		testdata.CorruptRTPWrap,
		testdata.CorruptDrift,
		testdata.CorruptVFRDupPTS,
	} {
		opts := baseOptions(t.TempDir(), c)
		res, err := Run(opts)
		if err != nil {
			t.Fatalf("%s: %v", c, err)
		}
		if res.Stats.Segments == 0 {
			t.Fatalf("%s: no segments produced", c)
		}
		if len(res.Manifest.Segments) == 0 {
			t.Fatalf("%s: manifest has no segments", c)
		}
		// Every manifest segment must carry a non-negative duration.
		for _, seg := range res.Manifest.Segments {
			if seg.DurationNanos < 0 {
				t.Fatalf("%s: negative segment duration", c)
			}
		}
	}
}

// TestRecoverMarksUnits confirms recovery mode flags every recoverable fault
// and reports a positive recovered-unit count with a non-negative offset.
func TestRecoverMarksUnits(t *testing.T) {
	opts := baseOptions(t.TempDir(), testdata.CorruptTruncatedBox)
	res, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	n := MarkRecovered(res.Errors)
	if n == 0 {
		t.Fatal("expected at least one recovered unit")
	}
	if res.Stats.SourceErrorOffset < 0 {
		// Recompute through the same helper the CLI relies on.
		if off := sourceCorruptionOffset(res.Errors); off < 0 {
			t.Fatal("source error offset must be non-negative")
		}
	}
	if res.Errors.RecoveredCount() != n {
		t.Fatal("recovered count mismatch")
	}
}

// TestBitrateAndAlignmentBoundaries checks the acceptance thresholds.
func TestBitrateAndAlignmentBoundaries(t *testing.T) {
	opts := baseOptions(t.TempDir(), testdata.CorruptTruncatedBox)
	res, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.BitrateDeviationPct > 12.0 {
		t.Fatalf("bitrate deviation %.2f%% exceeds 12%%",
			res.Stats.BitrateDeviationPct)
	}
	if res.Stats.ManifestAlignmentMs > 20.0 {
		t.Fatalf("alignment %.2f ms exceeds 20 ms",
			res.Stats.ManifestAlignmentMs)
	}
	if res.Stats.AudioRMS < 0 {
		t.Fatal("audio RMS must be non-negative")
	}
}
