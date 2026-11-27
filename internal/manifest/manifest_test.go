package manifest

import (
	"strings"
	"testing"
)

var pipeline = []string{"ingest", "reorder", "rtcp", "events", "align", "mux", "validate"}

func TestDeterministicBytes(t *testing.T) {
	build := func() *Manifest {
		m := New(7, 10000, 1700000000000000000, pipeline)
		m.SetStageInput("ingest", Digest([]byte("stream")), DigestStrings("params", "cap=4194304"))
		m.SetStage("ingest", StatusDone, "", "3 records")
		m.AddArtifact("b.bin", "aa", 2)
		m.AddArtifact("a.bin", "bb", 1)
		m.SortArtifacts()
		m.ComputeResumeStage()
		return m
	}
	a, b := build(), build()
	ab, err := a.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	bb, _ := b.Bytes()
	if string(ab) != string(bb) {
		t.Fatal("identical manifests must encode identically")
	}
	if Compare(a, b) != "" {
		t.Fatal("Compare should report no difference")
	}
	ha, err := a.SHA256()
	if err != nil {
		t.Fatalf("SHA256: %v", err)
	}
	hb, _ := b.SHA256()
	if ha != hb {
		t.Fatalf("hashes differ: %s vs %s", ha, hb)
	}
	if len(ha) != 64 {
		t.Fatalf("hash length = %d", len(ha))
	}
}

func TestCanonicalFieldOrder(t *testing.T) {
	m := New(1, 2, 3, pipeline)
	b, _ := m.Bytes()
	s := string(b)
	for _, key := range []string{`"schema_version"`, `"seed"`, `"budget_ms"`, `"stream_sha256"`, `"stages"`, `"artifacts"`, `"resume_stage"`} {
		if !strings.Contains(s, key) {
			t.Fatalf("manifest missing %s:\n%s", key, s)
		}
	}
	idx := func(k string) int { return strings.Index(s, k) }
	if !(idx(`"schema_version"`) < idx(`"seed"`) && idx(`"seed"`) < idx(`"budget_ms"`)) {
		t.Fatal("top level fields are not in declaration order")
	}
}

func TestResumeStage(t *testing.T) {
	m := New(1, 2, 3, pipeline)
	m.ComputeResumeStage()
	if m.ResumeStage != "ingest" {
		t.Fatalf("resume = %s, want ingest", m.ResumeStage)
	}
	m.SetStage("ingest", StatusDone, "x", "")
	m.SetStage("reorder", StatusDone, "y", "")
	m.ComputeResumeStage()
	if m.ResumeStage != "rtcp" {
		t.Fatalf("resume = %s, want rtcp", m.ResumeStage)
	}
	for _, s := range pipeline {
		m.SetStage(s, StatusDone, "z", "")
	}
	m.ComputeResumeStage()
	if m.ResumeStage != "complete" {
		t.Fatalf("resume = %s, want complete", m.ResumeStage)
	}
	if st, ok := m.StageStatus("mux"); !ok || st != StatusDone {
		t.Fatalf("mux status = %v, %v", st, ok)
	}
}

func TestParseRoundTrip(t *testing.T) {
	m := New(7, 10000, 0, pipeline)
	m.SetStage("ingest", StatusDone, "abc", "note")
	m.ComputeResumeStage()
	data, _ := m.Bytes()
	back, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if Compare(m, back) != "" {
		t.Fatal("round trip changed the manifest")
	}
	if _, err := Parse([]byte(`{"schema_version": 99}`)); err == nil {
		t.Fatal("unsupported schema version should fail")
	}
	if _, err := Parse([]byte("not json")); err == nil {
		t.Fatal("invalid json should fail")
	}
}

func TestDigestStability(t *testing.T) {
	if Digest([]byte("x")) != Digest([]byte("x")) {
		t.Fatal("digest not stable")
	}
	if Digest([]byte("x")) == Digest([]byte("y")) {
		t.Fatal("digest collision")
	}
	if DigestStrings("t", "a") == DigestStrings("t", "a", "") {
		t.Fatal("empty value must change the digest")
	}
}

func TestCompareDetectsDifference(t *testing.T) {
	a := New(1, 2, 3, pipeline)
	b := New(1, 2, 3, pipeline)
	b.SetStage("ingest", StatusDone, "x", "")
	if Compare(a, b) == "" {
		t.Fatal("Compare should detect a difference")
	}
}
