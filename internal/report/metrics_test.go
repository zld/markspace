package report

import (
	"encoding/json"
	"math"
	"testing"

	"mediacheck/internal/dash"
	"mediacheck/internal/fixture"
	"mediacheck/internal/model"
	"mediacheck/internal/normalize"
	"mediacheck/internal/probe"
	"mediacheck/internal/safeio"
)

func buildInputs(t *testing.T, faults []model.FaultKind) Inputs {
	t.Helper()
	prog := fixture.Generate(17, faults)
	if err := fixture.Materialize(prog); err != nil {
		t.Fatal(err)
	}
	manifest, err := dash.ParseMPD(prog, safeio.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	pr := probe.Inspect(manifest, prog)
	nr := normalize.Validate(manifest)
	return Inputs{Manifest: manifest, Probe: pr, Normalize: nr}
}

func TestHealthyLineThresholds(t *testing.T) {
	line, err := Build(buildInputs(t, nil), safeio.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(line.BitrateDeviationPct) > 5.0 {
		t.Fatalf("deviation %.3f%% > 5%%", line.BitrateDeviationPct)
	}
	if math.IsNaN(line.QualityMean) || math.IsInf(line.QualityMean, 0) {
		t.Fatal("quality mean not finite")
	}
	if math.IsNaN(line.QualityP95) || math.IsInf(line.QualityP95, 0) {
		t.Fatal("quality p95 not finite")
	}
	if line.ManifestAlignmentMs > 20.0 {
		t.Fatalf("alignment %.3f ms > 20", line.ManifestAlignmentMs)
	}
	if len(line.DamagedRanges) != 0 {
		t.Fatalf("healthy run has damage: %+v", line.DamagedRanges)
	}
	if line.ReportSHA256 == "" {
		t.Fatal("missing report hash")
	}
}

func TestDamagedLineLocatesRangesAndConflicts(t *testing.T) {
	line, err := Build(buildInputs(t, []model.FaultKind{
		model.FaultTruncate, model.FaultLate, model.FaultMetaConflict,
	}), safeio.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(line.DamagedRanges) < 2 {
		t.Fatalf("want >=2 damaged ranges, got %d", len(line.DamagedRanges))
	}
	fields := map[normalize.Field]bool{}
	for _, c := range line.MetadataConflicts {
		fields[c.Field] = true
	}
	for _, want := range []normalize.Field{
		normalize.FieldChannels, normalize.FieldSampleRate, normalize.FieldColor,
	} {
		if !fields[want] {
			t.Fatalf("missing metadata conflict %s", want)
		}
	}
}

func TestDeterministicHash(t *testing.T) {
	in := buildInputs(t, []model.FaultKind{
		model.FaultTruncate, model.FaultLate, model.FaultMetaConflict,
	})
	first, err := Build(in, safeio.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(buildInputs(t, []model.FaultKind{
		model.FaultTruncate, model.FaultLate, model.FaultMetaConflict,
	}), safeio.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if first.ReportSHA256 != second.ReportSHA256 {
		t.Fatalf("hash drift: %s != %s", first.ReportSHA256, second.ReportSHA256)
	}
	b1, _ := first.Marshal()
	b2, _ := second.Marshal()
	if string(b1) != string(b2) {
		t.Fatal("serialized lines differ")
	}
}

func TestLineIsSingleValidJSON(t *testing.T) {
	line, err := Build(buildInputs(t, nil), safeio.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	b, err := line.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	for _, key := range []string{
		"target_bitrate_bps", "actual_bitrate_bps", "bitrate_deviation_pct",
		"quality_mean", "quality_p95", "manifest_alignment_ms",
		"damaged_ranges", "recovered_transactions", "report_sha256",
	} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("missing field %s", key)
		}
	}
}

func TestReportSizeLimit(t *testing.T) {
	lim := safeio.DefaultLimits()
	lim.MaxReportBytes = 10
	if _, err := Build(buildInputs(t, nil), lim); err != model.ErrReportTooLarge {
		t.Fatalf("want report-too-large, got %v", err)
	}
}
