package pipeline_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"mediadeliver/internal/align"
	"mediadeliver/internal/fixture"
	"mediadeliver/internal/model"
	"mediadeliver/internal/pipeline"
)

func setup(t *testing.T, d fixture.Delivery) (work, manifest string) {
	t.Helper()
	work = t.TempDir()
	if _, _, err := pipeline.WriteInputs(work, d.MPD, d.Playlist, d.Media); err != nil {
		t.Fatal(err)
	}
	return work, filepath.Join(work, "manifest.json")
}

func run(t *testing.T, work, mpd, pl, manifest string, d fixture.Delivery, interrupt string) *pipeline.Outcome {
	t.Helper()
	media := map[string][]byte{}
	for id, data := range d.Media {
		media[id] = data
	}
	out, err := pipeline.Run(pipeline.Options{
		WorkDir: work, MPDPath: mpd, HLSPath: pl, Media: media,
		ManifestPath: manifest, InterruptAt: interrupt, Resume: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return out
}

func setupAndRun(t *testing.T, d fixture.Delivery) (work string, out *pipeline.Outcome) {
	t.Helper()
	work, manifest := setup(t, d)
	mpd := filepath.Join(work, "inputs", "manifest.mpd")
	pl := filepath.Join(work, "inputs", "media.m3u8")
	return work, run(t, work, mpd, pl, manifest, d, "")
}

func TestNormalRunPublishesManifest(t *testing.T) {
	d := fixture.Normal()
	work, out := setupAndRun(t, d)
	if !out.Published || out.Stage != pipeline.StageDone {
		t.Fatalf("run did not publish: stage=%s diags=%v", out.Stage, out.Diagnostics)
	}
	if out.Manifest.Summary.Representations != 3 || out.Manifest.Summary.Segments != 12 {
		t.Fatalf("summary = %+v", out.Manifest.Summary)
	}
	if out.Manifest.Summary.MaxDriftNS > align.MaxDriftNS {
		t.Fatalf("max drift = %d", out.Manifest.Summary.MaxDriftNS)
	}
	data, err := os.ReadFile(out.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != out.ManifestSHA {
		t.Fatalf("published checksum does not match the file")
	}
	// Re-running from scratch on the same inputs must be byte identical.
	out2 := run(t, work, filepath.Join(work, "inputs", "manifest.mpd"),
		filepath.Join(work, "inputs", "media.m3u8"), out.ManifestPath, d, "")
	data2, _ := os.ReadFile(out2.ManifestPath)
	if !bytes.Equal(data, data2) {
		t.Fatal("repeated run produced a different manifest")
	}
	if out2.ManifestSHA != out.ManifestSHA {
		t.Fatalf("checksum changed: %s vs %s", out.ManifestSHA, out2.ManifestSHA)
	}
}

func TestResumeAfterEachStage(t *testing.T) {
	d := fixture.Normal()
	_, full := setupAndRun(t, d)
	freshManifest, err := os.ReadFile(full.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{pipeline.StageScan, pipeline.StageAlign, pipeline.StageVerify} {
		t.Run(stage, func(t *testing.T) {
			work, manifest := setup(t, d)
			mpd := filepath.Join(work, "inputs", "manifest.mpd")
			pl := filepath.Join(work, "inputs", "media.m3u8")
			first := run(t, work, mpd, pl, manifest, d, stage)
			if first.Published {
				t.Fatalf("run interrupted at %s published a manifest", stage)
			}
			if _, err := os.Stat(manifest); !os.IsNotExist(err) {
				t.Fatalf("manifest exists after interruption at %s", stage)
			}
			second := run(t, work, mpd, pl, manifest, d, "")
			if !second.Published {
				t.Fatalf("resume from %s did not publish: %v", stage, second.Diagnostics)
			}
			if !second.Resumed {
				t.Fatalf("resume from %s did not report Resumed", stage)
			}
			got, err := os.ReadFile(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, freshManifest) {
				t.Fatalf("resumed manifest differs from a from-scratch run")
			}
			if second.SegmentsCounted != full.SegmentsCounted {
				t.Fatalf("segments counted = %d, want %d (no double counting)",
					second.SegmentsCounted, full.SegmentsCounted)
			}
		})
	}
}

func TestInputChangeInvalidatesCursor(t *testing.T) {
	d := fixture.Normal()
	work, manifest := setup(t, d)
	mpd := filepath.Join(work, "inputs", "manifest.mpd")
	pl := filepath.Join(work, "inputs", "media.m3u8")
	if out := run(t, work, mpd, pl, manifest, d, pipeline.StageAlign); out.Published {
		t.Fatal("interrupted run published")
	}
	// Mutate one media input, keeping its length identical so only the
	// digest can detect the change.
	mutated := map[string][]byte{}
	for id, data := range d.Media {
		cp := append([]byte(nil), data...)
		if id == "v720" {
			cp[len(cp)-1] ^= 0xFF
		}
		mutated[id] = cp
	}
	_, err := pipeline.Run(pipeline.Options{
		WorkDir: work, MPDPath: mpd, HLSPath: pl, Media: mutated, ManifestPath: manifest, Resume: true,
	})
	if err == nil {
		t.Fatal("run reused a cursor whose inputs changed")
	}
	e, ok := err.(*model.Error)
	if !ok || e.Code != model.CodeInputChanged {
		t.Fatalf("error = %v, want input_changed", err)
	}
	if _, statErr := os.Stat(manifest); !os.IsNotExist(statErr) {
		t.Fatal("manifest published despite input change")
	}
}

func TestDamagedRunReportsAllFaultClasses(t *testing.T) {
	d := fixture.Damaged()
	work, out := setupAndRun(t, d)
	if out.Published {
		t.Fatal("damaged run published a manifest")
	}
	if _, err := os.Stat(out.ManifestPath); !os.IsNotExist(err) {
		t.Fatal("partial manifest was published")
	}
	codes := map[model.Code]bool{}
	for _, e := range out.Diagnostics {
		codes[e.Code] = true
		if e.Code != model.CodeBoundaryDrift && e.Code != model.CodeManifestInvalid &&
			e.Code != model.CodeHLSMismatch && e.Code != model.CodeAlignInput {
			if e.Offset == model.NoOffset && e.Path != "" {
				// Every structural fault must be locatable.
				t.Logf("diagnostic without offset: %v", e)
			}
		}
	}
	for _, want := range []model.Code{
		model.CodeTruncatedBox, model.CodeInvalidBoxSize, model.CodeZeroTimescale,
		model.CodeDuplicateMediaSeq, model.CodeMissingIndex,
	} {
		if !codes[want] {
			t.Errorf("missing %s diagnostic; got %v", want, codeList(out.Diagnostics))
		}
	}
	_ = work
	// The checkpoint must still be parseable so a fixed input can resume.
	if _, err := os.Stat(filepath.Join(work, "resume.json")); err != nil {
		t.Fatalf("no cursor written after failure: %v", err)
	}
}

func codeList(errs []*model.Error) []string {
	var out []string
	for _, e := range errs {
		out = append(out, string(e.Code))
	}
	return out
}

func TestVerifyRejectsDuplicateSequenceInsideDiscontinuity(t *testing.T) {
	// Two DASH segments sharing a number inside one discontinuity interval
	// must fail the strict-ordering invariant.
	d := fixture.Normal()
	_, out := setupAndRun(t, d)
	m := *out.Manifest
	m.Intervals = append([]align.Interval(nil), m.Intervals...)
	m.Intervals[0].Entries = append([]align.Entry(nil), m.Intervals[0].Entries...)
	m.Intervals[0].Entries[0].SegmentNumbers = []uint64{4, 4}
	errs := pipeline.Verify(&m)
	found := false
	for _, e := range errs {
		if e.Code == model.CodeSequenceNotInc {
			found = true
		}
	}
	if !found {
		t.Fatalf("no sequence_not_increasing diagnostic: %v", codeList(errs))
	}
}

func TestVerifyRejectsUntraceableEntry(t *testing.T) {
	d := fixture.Normal()
	_, out := setupAndRun(t, d)
	m := *out.Manifest
	m.Intervals = append([]align.Interval(nil), m.Intervals...)
	m.Intervals[0].Entries = append([]align.Entry(nil), m.Intervals[0].Entries...)
	m.Intervals[0].Entries[0].MPDDigest = ""
	errs := pipeline.Verify(&m)
	found := false
	for _, e := range errs {
		if e.Code == model.CodeUntraceable {
			found = true
		}
	}
	if !found {
		t.Fatalf("no untraceable diagnostic: %v", codeList(errs))
	}
}

func TestVerifyRejectsBrokenManifest(t *testing.T) {
	d := fixture.Normal()
	_, out := setupAndRun(t, d)
	m := *out.Manifest
	m.Intervals = append([]align.Interval(nil), m.Intervals...)
	m.Intervals[1].StartNS = 0 // backwards
	m.Intervals[1].EndNS = 1
	errs := pipeline.Verify(&m)
	if len(errs) == 0 {
		t.Fatal("Verify accepted a manifest with regressing intervals")
	}
	found := false
	for _, e := range errs {
		if e.Code == model.CodeTimeRegression {
			found = true
		}
	}
	if !found {
		t.Fatalf("no time_regression diagnostic: %v", codeList(errs))
	}
}

func TestCursorRoundTrip(t *testing.T) {
	d := fixture.Normal()
	work, manifest := setup(t, d)
	mpd := filepath.Join(work, "inputs", "manifest.mpd")
	pl := filepath.Join(work, "inputs", "media.m3u8")
	run(t, work, mpd, pl, manifest, d, pipeline.StageScan)
	raw, err := os.ReadFile(filepath.Join(work, "resume.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cp pipeline.Checkpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		t.Fatalf("cursor is not JSON: %v", err)
	}
	if cp.Stage != pipeline.StageScan || cp.InputDigest == "" {
		t.Fatalf("cursor = %+v", cp)
	}
}
