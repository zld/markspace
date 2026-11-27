package provenance

import (
	"os"
	"path/filepath"
	"testing"

	"mediacheck/internal/fixture"
	"mediacheck/internal/index"
	"mediacheck/internal/manifest"
	"mediacheck/internal/timeline"
)

func buildResult(t *testing.T, dir string, c fixture.Case) *Result {
	t.Helper()
	if err := fixture.Generate(dir, c); err != nil {
		t.Fatal(err)
	}
	var events []manifest.Event
	for _, p := range []string{"fixtures/hls/playlist.m3u8", "fixtures/dash/manifest.mpd"} {
		full := filepath.Join(dir, p)
		var evs []manifest.Event
		var err error
		if filepath.Ext(p) == ".m3u8" {
			evs, err = manifest.ParseHLS(full)
		} else {
			evs, err = manifest.ParseDASH(full)
		}
		if err != nil {
			t.Fatal(err)
		}
		for i := range evs {
			evs[i].ManifestPath, _ = filepath.Rel(dir, evs[i].ManifestPath)
			evs[i].SegmentPath, _ = filepath.Rel(dir, evs[i].SegmentPath)
		}
		events = append(events, evs...)
	}
	entries, err := index.MapEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	ix, err := index.Assemble(dir, entries)
	if err != nil {
		t.Fatal(err)
	}
	var inputs []string
	for _, e := range events {
		inputs = append(inputs, e.SegmentPath)
	}
	inputs = append(inputs, "fixtures/hls/playlist.m3u8", "fixtures/dash/manifest.mpd")
	id, err := ComputeIdentity(dir, inputs, ConfigDigest("format=both"))
	if err != nil {
		t.Fatal(err)
	}
	return &Result{Identity: id, Events: len(events), Index: ix}
}

func TestVerifyClean(t *testing.T) {
	dir := t.TempDir()
	res := buildResult(t, dir, fixture.CaseNormal)
	findings, err := Verify(dir, res)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("clean result has findings: %+v", findings)
	}
}

func TestVerifyTruncated(t *testing.T) {
	dir := t.TempDir()
	res := buildResult(t, dir, fixture.CaseTruncated)
	findings, err := Verify(dir, res)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Kind != timeline.KindTruncated {
		t.Fatalf("findings = %+v", findings)
	}
	if findings[0].StartNs != 1_000_000_000 || findings[0].EndNs != 2_000_000_000 {
		t.Fatalf("finding range = [%d,%d)", findings[0].StartNs, findings[0].EndNs)
	}
	if n := AffectedEntries(res, findings); n != 1 {
		t.Fatalf("affected = %d, want 1", n)
	}
}

func TestVerifyDigestMismatch(t *testing.T) {
	dir := t.TempDir()
	res := buildResult(t, dir, fixture.CaseNormal)
	// corrupt a segment after the result was committed
	f, err := os.OpenFile(filepath.Join(dir, "fixtures/hls/seg0.m4s"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte{0})
	f.Close()
	findings, err := Verify(dir, res)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []timeline.Kind
	for _, f := range findings {
		kinds = append(kinds, f.Kind)
	}
	if len(findings) == 0 {
		t.Fatal("corruption not detected")
	}
	for _, k := range kinds {
		if k != timeline.KindDigestMismatch {
			t.Fatalf("unexpected finding kinds: %v", kinds)
		}
	}
}

func TestIdentityDiff(t *testing.T) {
	dir := t.TempDir()
	res := buildResult(t, dir, fixture.CaseNormal)
	id2, err := ComputeIdentity(dir, []string{"fixtures/hls/playlist.m3u8"}, ConfigDigest("format=hls"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Identity.Digest() == id2.Digest() {
		t.Fatal("different identities share a digest")
	}
	if d := DiffInputs(res.Identity, id2); d == "none" {
		t.Fatal("diff not detected")
	}
	if d := DiffInputs(res.Identity, res.Identity); d != "none" {
		t.Fatalf("self diff = %s", d)
	}
}
