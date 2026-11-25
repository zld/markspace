package stream_test

import (
	"bytes"
	"testing"

	"example.com/assetcheck/internal/clock"
	"example.com/assetcheck/internal/fault"
	"example.com/assetcheck/internal/fixture"
	"example.com/assetcheck/internal/index"
	"example.com/assetcheck/internal/report"
	"example.com/assetcheck/internal/scan"
	"example.com/assetcheck/internal/stream"
	"example.com/assetcheck/internal/validate"
)

// pipeline is one full local run over a generated asset.
type pipeline struct {
	asset  fixture.Asset
	scan   *scan.Result
	valid  *validate.Result
	stream *stream.Outcome
	text   string
	digest uint64
}

func runPipeline(t *testing.T, a fixture.Asset) *pipeline {
	t.Helper()
	res := scan.Run(scan.Input{MP4: a.MP4, RTP: a.RTP})
	p := &pipeline{asset: a, scan: res}
	if res.Index.Complete {
		p.valid = validate.Check(res.Index, a.MP4)
	}
	p.stream = stream.Run(res.Index, a.MP4)
	run := &report.Run{
		Scenario: string(a.Scenario),
		Index:    res.Index,
		Scan:     res.Faults,
		Validate: p.valid,
		Stream:   p.stream,
	}
	var buf bytes.Buffer
	if err := report.Write(&buf, run); err != nil {
		t.Fatalf("report: %v", err)
	}
	p.text = buf.String()
	p.digest, _ = report.Summarize(run, merged(run))
	return p
}

func merged(r *report.Run) []*fault.Error {
	list := append([]*fault.Error{}, r.Scan...)
	if r.Validate != nil {
		list = append(list, r.Validate.Faults...)
	}
	if r.Stream != nil {
		list = append(list, r.Stream.Faults...)
	}
	fault.Sort(list)
	return list
}

func findByScenario(t *testing.T, name fixture.Scenario) *pipeline {
	t.Helper()
	for _, a := range fixture.Build(7) {
		if a.Scenario == name {
			return runPipeline(t, a)
		}
	}
	t.Fatalf("scenario %s not generated", name)
	return nil
}

func TestNormalHasZeroDeviation(t *testing.T) {
	p := findByScenario(t, fixture.ScenarioNormal)
	if p.valid == nil {
		t.Fatal("normal asset should validate")
	}
	if len(merged(&report.Run{Index: p.scan.Index, Scan: p.scan.Faults, Validate: p.valid, Stream: p.stream})) != 0 {
		t.Fatalf("normal asset reported faults:\n%s", p.text)
	}
	if p.stream.ByteDev != 0 || p.stream.TimeDev != 0 {
		t.Fatalf("stream deviation = %d bytes, %v", p.stream.ByteDev, p.stream.TimeDev)
	}
	for _, m := range p.valid.Mappings {
		if !m.Dev.OK || m.Dev.ByteDelta != 0 || m.Dev.TimeDelta != 0 {
			t.Fatalf("sample %d deviates: %+v", m.Sample, m.Dev)
		}
		if !m.HasPkt {
			t.Fatalf("sample %d has no packet", m.Sample)
		}
	}
	if p.stream.Confirmed != totalSamples(p.scan.Index) {
		t.Fatalf("confirmed = %d of %d", p.stream.Confirmed, totalSamples(p.scan.Index))
	}
	if !p.stream.Completed {
		t.Fatal("normal stream should complete")
	}
}

func TestDuplicateAddsNoTime(t *testing.T) {
	p := findByScenario(t, fixture.ScenarioDuplicate)
	if p.valid == nil || p.valid.Duplicate != 1 {
		t.Fatalf("duplicate count = %v", p.valid)
	}
	if p.valid.Skew != 0 {
		t.Fatalf("duplicate changed skew to %v", p.valid.Skew)
	}
	for _, pkt := range p.scan.Index.Packets {
		if pkt.Kind == index.PacketDuplicate && pkt.Duration != 0 {
			t.Fatalf("duplicate advanced media time by %v", pkt.Duration)
		}
	}
	if p.stream.Confirmed != totalSamples(p.scan.Index) {
		t.Fatalf("confirmed = %d, duplicate should not displace a sample", p.stream.Confirmed)
	}
}

func TestMissingKeepsGapRecord(t *testing.T) {
	p := findByScenario(t, fixture.ScenarioMissing)
	if p.valid == nil {
		t.Fatal("missing asset should still validate structurally")
	}
	if p.valid.Missing != 1 {
		t.Fatalf("missing = %d, want 1", p.valid.Missing)
	}
	gaps := 0
	for _, pkt := range p.scan.Index.Packets {
		if pkt.Kind == index.PacketGap {
			gaps++
		}
	}
	if gaps != 1 {
		t.Fatalf("gap records = %d, want 1", gaps)
	}
	// The gap must be represented in time, and not fabricated as audio.
	var gapFault *fault.Error
	for _, f := range p.valid.Faults {
		if f.Code == "validate.packet.gap" {
			gapFault = f
		}
	}
	if gapFault == nil {
		t.Fatalf("no located gap fault:\n%s", p.text)
	}
	if gapFault.Time == clock.Invalid {
		t.Fatal("gap fault has no time position")
	}
	if p.stream.Completed {
		t.Fatal("stream should not complete when a packet is missing")
	}
	if p.stream.Failed == 0 {
		t.Fatal("the failed sample must not be counted as confirmed")
	}
}

func TestTruncatedAndCorruptAreLocated(t *testing.T) {
	for _, name := range []fixture.Scenario{fixture.ScenarioTruncated, fixture.ScenarioCorrupt} {
		p := findByScenario(t, name)
		if p.scan.Index.Complete {
			t.Fatalf("%s: expected an incomplete scan", name)
		}
		if len(p.scan.Faults) == 0 {
			t.Fatalf("%s: expected a located scan fault", name)
		}
		for _, f := range p.scan.Faults {
			if f.Offset < 0 {
				t.Fatalf("%s: fault lacks a byte offset: %v", name, f)
			}
		}
		if p.stream.Completed {
			t.Fatalf("%s: stream must not complete", name)
		}
		// Confirmed records before the failure are still reported.
		if p.stream.Confirmed == 0 {
			t.Fatalf("%s: expected confirmed records before the failure", name)
		}
	}
}

func TestIndexBoundToDigest(t *testing.T) {
	p := findByScenario(t, fixture.ScenarioNormal)
	idx := p.scan.Index
	dir := t.TempDir()
	path := dir + "/index.json"
	if err := idx.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := index.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Digest.Value != idx.Digest.Value {
		t.Fatal("saved index lost its digest")
	}
	// A changed input yields a different digest, so the old index is not
	// applicable.
	changed := append(append([]byte{}, p.asset.MP4...), 0x00)
	newDigest, _ := index.ComputeDigest(changed, p.asset.RTP)
	if newDigest.Value == loaded.Digest.Value {
		t.Fatal("digest did not change with the input")
	}
	// A structurally broken index is rejected on load.
	if err := idx.CheckStructure(); err != nil {
		t.Fatalf("complete index failed its own check: %v", err)
	}
	broken := *idx
	broken.Complete = false
	if err := broken.CheckStructure(); err == nil {
		t.Fatal("incomplete index should be rejected")
	}
}

func TestRepeatedRunsAreStable(t *testing.T) {
	for _, a := range fixture.Build(7) {
		first := runPipeline(t, a)
		second := runPipeline(t, a)
		if first.digest != second.digest {
			t.Fatalf("%s: summary digest changed: %x vs %x", a.Scenario, first.digest, second.digest)
		}
		if first.text != second.text {
			t.Fatalf("%s: rendered report changed between runs", a.Scenario)
		}
		if got, want := faultStrings(first.scan.Faults), faultStrings(second.scan.Faults); !equalStrings(got, want) {
			t.Fatalf("%s: fault order changed", a.Scenario)
		}
	}
}

func TestSummaryDigestNamesAlgorithm(t *testing.T) {
	if report.SummaryDigestVersion != index.ReportDigestAlgorithm {
		t.Fatalf("report names %q, index names %q", report.SummaryDigestVersion, index.ReportDigestAlgorithm)
	}
	if index.ReportDigestAlgorithm != "fnv1a64/v1" {
		t.Fatalf("unexpected report algorithm %q", index.ReportDigestAlgorithm)
	}
	// A different asset must produce a different summary.
	normal := findByScenario(t, fixture.ScenarioNormal)
	missing := findByScenario(t, fixture.ScenarioMissing)
	if normal.digest == missing.digest {
		t.Fatal("distinct assets produced the same summary digest")
	}
}

func TestStreamRejectsNilIndex(t *testing.T) {
	out := stream.Run(nil, nil)
	if out.Completed || out.FirstFail == nil {
		t.Fatal("nil index must fail the stream")
	}
}

func totalSamples(idx *index.Index) int {
	n := 0
	for _, tr := range idx.Tracks {
		n += len(tr.Samples)
	}
	return n
}

func faultStrings(list []*fault.Error) []string { return fault.Strings(list) }

func equalStrings(a, b []string) bool {
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
