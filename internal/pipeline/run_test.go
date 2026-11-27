package pipeline

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"example.com/assetpack/internal/audio"
	"example.com/assetpack/internal/fixture"
	"example.com/assetpack/internal/manifest"
	"example.com/assetpack/internal/migrate"
	"example.com/assetpack/internal/ts"
)

// setup writes the healthy fixture inputs into a fresh work directory.  The
// legacy manifest always records the *clean* stream digests, so injecting
// faults makes the source disagree with the manifest — exactly the situation
// a real damaged hand-off creates.
func setup(t *testing.T, dir string, inject fixture.FaultSet) Options {
	return setupWith(t, dir, inject, false)
}

// setupWith optionally records the digests of the bytes actually written,
// which lets a test exercise downstream stages against a damaged-but-
// declared source.
func setupWith(t *testing.T, dir string, inject fixture.FaultSet, recordActual bool) Options {
	t.Helper()
	inputDir := filepath.Join(dir, "input")
	if err := os.MkdirAll(inputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	specs := fixture.DefaultAssets()
	clean := fixture.TSFiles(specs)
	for i, spec := range specs {
		key := spec.Name + ".ts"
		data := clean[key]
		if len(inject.Names()) > 0 {
			opts := fixture.StreamOptions{Asset: spec, PID: uint16(0x102 + i),
				PCRPID: uint16(0x201 + i), PMTPID: uint16(0x1000 + i), Program: uint16(i + 1)}
			data = fixture.BuildBroken(opts, inject).Data
		}
		if err := os.WriteFile(filepath.Join(inputDir, key), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	source := clean
	if recordActual {
		source = make(map[string][]byte, len(specs))
		for _, spec := range specs {
			key := spec.Name + ".ts"
			raw, err := os.ReadFile(filepath.Join(inputDir, key))
			if err != nil {
				t.Fatal(err)
			}
			source[key] = raw
		}
	}
	legacy := fixture.LegacyFor("test-release", specs, source)
	raw, _ := legacy.Encode()
	if err := os.WriteFile(filepath.Join(inputDir, "manifest.v1.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return Options{
		WorkDir: dir, InputDir: inputDir,
		ReleaseDir: filepath.Join(dir, "release"), StateDir: filepath.Join(dir, "state"),
		Release: "test-release",
		Target:  audio.Target{Format: audio.Format{Rate: 48000, Layout: audio.LayoutStereo}, Loudness: -16},
	}
}

func TestRunSuccess(t *testing.T) {
	dir := t.TempDir()
	opts := setup(t, dir, fixture.FaultSet{})
	out, err := Run(opts)
	if err != nil {
		t.Fatalf("run failed: %v\n%s", err, strings.Join(out.Journal.Summary(), "\n"))
	}
	if !out.Valid || !out.Committed || out.ExitCode() != 0 {
		t.Fatalf("outcome not valid: %+v", out)
	}
	if out.Journal.CompletedCount() != 5 {
		t.Fatalf("completed %d stages, want 5", out.Journal.CompletedCount())
	}
	if len(out.Assets) != 3 {
		t.Fatalf("assets %d", len(out.Assets))
	}
	for _, a := range out.Assets {
		if a.Report.OutRate != 48000 || a.Report.OutLayout != "stereo" {
			t.Fatalf("asset %s not normalized: %+v", a.Name, a.Report)
		}
		if a.Index.Frames != a.Report.OutFrames || a.Index.Rate != 48000 {
			t.Fatalf("index mismatch for %s: %+v", a.Name, a.Index)
		}
		if a.PMTVersion != 0 || a.PCRWraps != 0 {
			t.Fatalf("unexpected PMT/PCR state: %+v", a)
		}
		if a.ParseOffset <= 0 {
			t.Fatalf("no parse resume offset for %s", a.Name)
		}
	}
	if out.ManifestSHA == "" || out.After == nil || out.After.Digest != out.ManifestSHA {
		t.Fatal("manifest digest not recorded")
	}
	if out.Diff.Added != 3 || out.Diff.Reused != 0 {
		t.Fatalf("first run diff %+v", out.Diff)
	}
	// The manifest on disk parses and verifies.
	cp := filepath.Join(opts.ReleaseDir, "manifest.json")
	raw, err := os.ReadFile(cp)
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.ReadV2(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("committed manifest invalid: %v", err)
	}
	if m.Digest != out.ManifestSHA {
		t.Fatalf("digest %s vs %s", m.Digest, out.ManifestSHA)
	}
}

func TestRunRerunReusesEverything(t *testing.T) {
	dir := t.TempDir()
	opts := setup(t, dir, fixture.FaultSet{})
	first, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if second.Diff.Added != 0 {
		t.Fatalf("rerun added %d resources", second.Diff.Added)
	}
	if second.Diff.Reused != len(second.Before.Resources) {
		t.Fatalf("rerun reused %d, want %d", second.Diff.Reused, len(second.Before.Resources))
	}
	if second.ManifestSHA != first.ManifestSHA {
		t.Fatalf("manifest digest changed: %s vs %s", second.ManifestSHA, first.ManifestSHA)
	}
	if second.After.Sequence != first.After.Sequence {
		t.Fatalf("sequence advanced on a no-op rerun: %d", second.After.Sequence)
	}
	if !second.Valid || second.ExitCode() != 0 {
		t.Fatalf("rerun not valid: %+v", second)
	}
}

func TestRunForceDoesNotDuplicate(t *testing.T) {
	dir := t.TempDir()
	opts := setup(t, dir, fixture.FaultSet{})
	first, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	forced := opts
	forced.Force = true
	out, err := Run(forced)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.After.Resources) != len(first.After.Resources) {
		t.Fatalf("forced rerun changed resource count %d -> %d", len(first.After.Resources), len(out.After.Resources))
	}
	if out.Diff.Added != 0 {
		t.Fatalf("forced rerun appended %d resources", out.Diff.Added)
	}
	if out.ManifestSHA != first.ManifestSHA {
		t.Fatalf("forced rerun changed the digest %s vs %s", out.ManifestSHA, first.ManifestSHA)
	}
}

func TestRunPathEscapeRejected(t *testing.T) {
	dir := t.TempDir()
	opts := setup(t, dir, fixture.FaultSet{})
	opts.ReleaseDir = filepath.Join(dir, "..", "escape")
	if _, err := Run(opts); err == nil {
		t.Fatal("path escape accepted")
	}
}

func TestRunInvalidTargetRejected(t *testing.T) {
	dir := t.TempDir()
	opts := setup(t, dir, fixture.FaultSet{})
	opts.Target.Format.Layout = "octaphonic"
	if _, err := Run(opts); err == nil {
		t.Fatal("invalid target accepted")
	}
}

func TestManifestIntervalsCoverFrames(t *testing.T) {
	dir := t.TempDir()
	opts := setup(t, dir, fixture.FaultSet{})
	out, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range out.After.Resources {
		var cover int64
		for _, iv := range r.Media {
			cover += iv.LengthSamples
		}
		if cover+r.GapSamples != r.Frames {
			t.Fatalf("resource %s: intervals %d + gaps %d != frames %d", r.Name, cover, r.GapSamples, r.Frames)
		}
		if r.SourceVersion != 1 || r.Kind != "audio" {
			t.Fatalf("provenance missing: %+v", r)
		}
	}
}

func TestJournalPersistedOnDisk(t *testing.T) {
	dir := t.TempDir()
	opts := setup(t, dir, fixture.FaultSet{})
	if _, err := Run(opts); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(opts.StateDir, "stages.json"))
	if err != nil {
		t.Fatal(err)
	}
	var written migrate.Journal
	if err := json.Unmarshal(raw, &written); err != nil {
		t.Fatal(err)
	}
	if written.CompletedCount() != 5 {
		t.Fatalf("persisted journal shows %d complete", written.CompletedCount())
	}
}

func TestSourceGapPadReachesManifest(t *testing.T) {
	dir := t.TempDir()
	opts := setupWith(t, dir, fixture.FaultSet{CCJump: true}, true)
	// With a CC jump the recovered payload is short; the PTS-derived gap
	// must be recorded rather than silently shortening the timeline.
	out, err := Run(opts)
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	var gaps int64
	for _, a := range out.Assets {
		gaps += a.Report.GapSamples
	}
	if gaps == 0 {
		t.Fatal("no gap recorded for a stream with a continuity break")
	}
	for _, r := range out.After.Resources {
		if r.GapSamples != 0 {
			var cover int64
			for _, iv := range r.Media {
				cover += iv.LengthSamples
			}
			if cover+r.GapSamples != r.Frames {
				t.Fatalf("gapped resource %s mismatched: %d+%d != %d", r.Name, cover, r.GapSamples, r.Frames)
			}
		}
	}
}

func TestParseFaultsCarryOffsets(t *testing.T) {
	dir := t.TempDir()
	opts := setup(t, dir, fixture.FaultSet{TruncatedTS: true})
	out, _ := Run(opts)
	if out.Faults.Len() == 0 {
		t.Fatal("no faults surfaced")
	}
	for _, f := range out.Faults.Items {
		if f.Kind == "truncated-ts" {
			if f.Offset <= 0 || f.Offset%ts.PacketSize == ts.PacketSize/2 {
				// The truncation offset is mid-packet by construction.
				if f.Offset <= 0 {
					t.Fatalf("truncation fault offset %d", f.Offset)
				}
			}
		}
	}
}

// readManifestBytes returns the committed manifest file exactly as written.
func readManifestBytes(t *testing.T, opts Options) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(opts.ReleaseDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
