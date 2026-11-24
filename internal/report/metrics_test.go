package report_test

import (
	"bytes"
	"context"
	"math"
	"testing"

	"mediaroute/internal/fixture"
	"mediaroute/internal/model"
	"mediaroute/internal/pipeline"
	"mediaroute/internal/report"
)

func TestActualBPSExact(t *testing.T) {
	// 2,000,000 bytes over 8 seconds = 2,000,000 bps.
	bps, err := report.ActualBPS(2_000_000, 8_000_000)
	if err != nil {
		t.Fatalf("actual: %v", err)
	}
	if bps != 2_000_000 {
		t.Fatalf("got %d bps, want 2000000", bps)
	}
}

func TestActualBPSRejectsZeroDuration(t *testing.T) {
	if _, err := report.ActualBPS(100, 0); err == nil {
		t.Fatal("expected error for zero duration")
	}
}

func TestDeltaPct(t *testing.T) {
	d, err := report.DeltaPct(2_000_000, 2_020_000)
	if err != nil {
		t.Fatalf("delta: %v", err)
	}
	if math.Abs(d-1.0) > 1e-9 {
		t.Fatalf("got %f%% want 1%%", d)
	}
	if _, err := report.DeltaPct(0, 100); err == nil {
		t.Fatal("expected error for zero target")
	}
}

func TestValidPctRounding(t *testing.T) {
	if got := report.ValidPct(5, 6); got != 83.33 {
		t.Fatalf("got %v want 83.33", got)
	}
	if got := report.ValidPct(0, 0); got != 0 {
		t.Fatalf("got %v want 0", got)
	}
	if got := report.ValidPct(12, 12); got != 100 {
		t.Fatalf("got %v want 100", got)
	}
}

func TestComputeRejectsNonPositiveTarget(t *testing.T) {
	if _, err := report.Compute(report.Inputs{TargetBPS: 0}); err == nil {
		t.Fatal("expected rejection of zero target")
	}
}

func buildManifest(t *testing.T, d fixture.Damage, target int64) *model.Manifest {
	t.Helper()
	params := fixture.DefaultParams(17)
	params.TargetBPS = target
	g, err := fixture.Generate(params, d)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	proc, err := pipeline.NewProcessor(pipeline.Config{QueueCapacity: 2, MaxBoxBytes: 8 << 20})
	if err != nil {
		t.Fatalf("processor: %v", err)
	}
	out, err := proc.Process(context.Background(), g.Bytes, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	m, err := report.Build(report.BuildInputs{
		Name:         d.String(),
		Seed:         17,
		OutputSize:   uint64(len(out.Output)),
		SourceBytes:  uint64(len(g.Bytes)),
		OutputSHA256: out.SHA256,
		TargetBPS:    target,
		Source:       out.Source,
		Repairs:      out.Repairs,
		Index:        out.Index,
		Counts: report.Counts{
			MFHDGaps:     out.Counts.MFHDGaps,
			Duplicates:   out.Counts.Duplicates,
			RepairedTfdt: out.Counts.RepairedTfdt,
			Truncated:    out.Counts.Truncated,
			Dropped:      out.Counts.Dropped,
			Recovered:    out.Counts.Recovered,
		},
	})
	if err != nil {
		t.Fatalf("build manifest: %v", err)
	}
	return m
}

func TestManifestCleanCase(t *testing.T) {
	m := buildManifest(t, fixture.DamageNone, 2_000_000)
	if m.ValidSamplePct != 100 {
		t.Fatalf("clean valid_sample_pct = %v, want 100", m.ValidSamplePct)
	}
	if m.MFHDGaps != 0 || m.TruncatedBoxes != 0 || m.DroppedFragments != 0 {
		t.Fatalf("clean stream reported damage: %+v", m)
	}
	if math.Abs(m.BitrateDeltaPct) > 5 {
		t.Fatalf("bitrate delta %v%% exceeds 5%%", m.BitrateDeltaPct)
	}
	if m.ManifestAlignmentUS > 1 {
		t.Fatalf("alignment error %d us exceeds 1 us", m.ManifestAlignmentUS)
	}
	if len(m.Tracks) != 2 {
		t.Fatalf("want 2 tracks, got %d", len(m.Tracks))
	}
	for _, tm := range m.Tracks {
		if tm.DurationMicros <= 0 {
			t.Fatalf("track %d has non-positive duration", tm.TrackID)
		}
	}
}

func TestManifestGapCase(t *testing.T) {
	m := buildManifest(t, fixture.DamageSequenceGap, 2_000_000)
	if m.MFHDGaps != 1 {
		t.Fatalf("want 1 gap, got %d", m.MFHDGaps)
	}
	if m.ValidSamplePct != 100 {
		t.Fatalf("gap must not drop samples, got %v%%", m.ValidSamplePct)
	}
}

func TestManifestRegressionCase(t *testing.T) {
	m := buildManifest(t, fixture.DamageTfdtRegression, 2_000_000)
	if m.RepairedTfdt < 1 {
		t.Fatalf("want at least one repaired tfdt, got %d", m.RepairedTfdt)
	}
	if m.ManifestAlignmentUS > 1 {
		t.Fatalf("alignment %d us exceeds 1 us after repair", m.ManifestAlignmentUS)
	}
	if len(m.Repairs) == 0 {
		t.Fatal("repair records missing")
	}
	for _, r := range m.Repairs {
		if r.OriginalValue == r.RepairedValue {
			t.Fatalf("repair record shows no change: %+v", r)
		}
	}
}

func TestManifestTruncatedCase(t *testing.T) {
	m := buildManifest(t, fixture.DamageTruncatedBox, 2_000_000)
	if m.TruncatedBoxes != 1 {
		t.Fatalf("want 1 truncated box, got %d", m.TruncatedBoxes)
	}
	if m.RecoveredFragments < 1 {
		t.Fatalf("want at least 1 recovered fragment, got %d", m.RecoveredFragments)
	}
	if m.DroppedFragments != 1 {
		t.Fatalf("want 1 dropped fragment, got %d", m.DroppedFragments)
	}
	if len(m.BadFragments) != 1 {
		t.Fatalf("want 1 bad fragment record, got %d", len(m.BadFragments))
	}
	if m.ValidSamplePct >= 100 {
		t.Fatalf("truncated case should lose samples, got %v%%", m.ValidSamplePct)
	}
}

func TestManifestCorruptIndexCase(t *testing.T) {
	m := buildManifest(t, fixture.DamageCorruptIndex, 2_000_000)
	if !m.DamagedIndex {
		t.Fatal("manifest should flag the damaged existing index")
	}
	if len(m.IndexIssues) == 0 {
		t.Fatal("manifest should record index hint issues")
	}
}

func TestManifestDigestIsDeterministic(t *testing.T) {
	a := buildManifest(t, fixture.DamageNone, 2_000_000)
	b := buildManifest(t, fixture.DamageNone, 2_000_000)
	da, err := report.Digest(a)
	if err != nil {
		t.Fatal(err)
	}
	db, err := report.Digest(b)
	if err != nil {
		t.Fatal(err)
	}
	if da != db {
		t.Fatalf("identical inputs produced different digests: %s vs %s", da, db)
	}
	if len(da) != 64 {
		t.Fatalf("unexpected digest length %d", len(da))
	}
}

func TestManifestRefusesMissingInit(t *testing.T) {
	params := fixture.DefaultParams(17)
	params.TargetBPS = 2_000_000
	g, err := fixture.Generate(params, fixture.DamageMissingInit)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	proc, _ := pipeline.NewProcessor(pipeline.DefaultConfig())
	out, err := proc.Process(context.Background(), g.Bytes, nil)
	if err == nil {
		// Some stages may reject earlier; if not, manifest building must.
		_, err = report.Build(report.BuildInputs{
			TargetBPS: 2_000_000, Source: out.Source, Index: &model.Index{},
		})
	}
	if err == nil {
		t.Fatal("expected a failure for a stream with no init segment")
	}
	if _, ok := model.AsTaskError(err); !ok {
		t.Fatalf("expected TaskError, got %T", err)
	}
}

func TestAlignManifestDetectsMismatch(t *testing.T) {
	ix := &model.Index{Entries: []model.IndexEntry{
		{TrackID: 1, StartMicros: 0, EndMicros: 1_000_000},
	}}
	tracks := []model.TrackManifest{
		{TrackID: 1, StartMicros: 0, EndMicros: 1_500_000, Samples: 1},
	}
	if got := report.AlignManifest(ix, tracks); got != 500_000 {
		t.Fatalf("got %d us, want 500000", got)
	}
}

func TestBuildIndexDocumentStable(t *testing.T) {
	m := buildManifest(t, fixture.DamageNone, 2_000_000)
	doc, err := report.BuildIndexDocument("clean", m.Index)
	if err != nil {
		t.Fatalf("build doc: %v", err)
	}
	if len(doc.Entries) != m.IndexEntries {
		t.Fatalf("doc has %d entries, manifest says %d", len(doc.Entries), m.IndexEntries)
	}
	a, err := report.MarshalIndex(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	doc2, _ := report.BuildIndexDocument("clean", m.Index)
	b, _ := report.MarshalIndex(doc2)
	if !bytes.Equal(a, b) {
		t.Fatal("index document serialisation is not stable")
	}
}
