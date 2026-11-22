package pipeline

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/fixture"
	"mediad/internal/index"
	"mediad/internal/ingest"
)

// setup writes the demo fixtures into a temp root and returns the root, the
// manifest names and the generation stream.
func setup(t *testing.T) (string, []string, *fixture.Stream) {
	t.Helper()
	root := t.TempDir()
	stream, err := fixture.Generate(fixture.DemoDefaults())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range stream.Files {
		path := filepath.Join(root, filepath.FromSlash(f.Name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, f.Data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, stream.Names(), stream
}

func runner(root, cp string) *Runner {
	return &Runner{
		Root: root,
		Params: Parameters{
			Seed: 42, ClockRate: 90000, MasterTrack: "video",
			MaxAssetBytes: ingest.DefaultLimits().MaxAssetBytes,
		},
		Checkpoint:     cp,
		CheckpointRoot: root,
	}
}

func TestEndToEndAcceptance(t *testing.T) {
	root, names, stream := setup(t)
	out, err := runner(root, "").Run(names)
	if !errors.Is(err, ErrDamaged) {
		t.Fatalf("want ErrDamaged for the corrupt corpus, got %v", err)
	}
	s := out.Summary
	if s.Cues < 20 {
		t.Fatalf("cues=%d want >=20", s.Cues)
	}
	if s.Frames < 100 {
		t.Fatalf("frames=%d want >=100", s.Frames)
	}
	if s.KeyframeGaps < 4 {
		t.Fatalf("keyframe_gaps=%d", s.KeyframeGaps)
	}
	if s.CorruptRanges < 5 {
		t.Fatalf("corrupt_ranges=%d want >=5", s.CorruptRanges)
	}
	if s.InvariantsFailed != 0 {
		t.Fatalf("invariants failed=%d: %v", s.InvariantsFailed, out.Validate.Result.FailedChecks())
	}
	if s.SHA256 == "" || len(s.SHA256) != 64 {
		t.Fatalf("sha256=%q", s.SHA256)
	}
	// Every generated MP4 carries the same sample table, so each contributes
	// (frames/30 - 1) gaps even when its media bytes are damaged.
	mp4s := 0
	for _, f := range stream.Files {
		if f.Kind == fixture.KindMP4 || f.Kind == fixture.KindCorruptMP4 {
			mp4s++
		}
	}
	if got := mp4s * (stream.NormalFrames/30 - 1); s.KeyframeGaps != got {
		t.Fatalf("keyframe gaps=%d want %d from %d mp4s", s.KeyframeGaps, got, mp4s)
	}
	// Every injected fault class must appear in the diagnosed ranges.
	want := map[string]bool{
		"truncated_box": false, "mp4_length": false, "srt_time": false,
		"vtt_time": false, "rtp_sequence_gap": false, "node_drift": false,
		"timecode_label": false, "truncated_cue": false,
	}
	for _, rg := range out.Diagnose.Ranges {
		for _, c := range rg.Categories {
			if _, ok := want[c]; ok {
				want[c] = true
			}
		}
	}
	for cat, seen := range want {
		if !seen {
			t.Errorf("fault class %s was not localized", cat)
		}
	}
}

func TestOffsetsMatchGroundTruth(t *testing.T) {
	root, names, stream := setup(t)
	out, err := runner(root, "").Run(names)
	if err != nil && !errors.Is(err, ErrDamaged) {
		t.Fatal(err)
	}
	// Every injected fault must be localized to its exact start byte by an
	// issue of the same class, before range merging can widen the interval.
	for _, want := range stream.Expected {
		found := false
		for _, is := range out.Diagnose.Issues {
			if is.Asset == want.Asset && string(is.Category) == want.Category && is.Span.Start == want.Span.Start {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("fault %s/%s at %d was not localized exactly", want.Asset, want.Category, want.Span.Start)
		}
	}
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func TestRepeatRunIsByteStable(t *testing.T) {
	root, names, _ := setup(t)
	params := runner(root, "").Params
	out1, err1 := runner(root, "").Run(names)
	out2, err2 := runner(root, "").Run(names)
	if !errors.Is(err1, ErrDamaged) || !errors.Is(err2, ErrDamaged) {
		t.Fatalf("run errors: %v %v", err1, err2)
	}
	if out1 == nil || out2 == nil {
		t.Fatal("nil output")
	}
	var a, b bytes.Buffer
	if err := WriteJSONL(&a, out1, params, out1.InputDigest, out1.ParamsDigest); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSONL(&b, out2, params, out2.InputDigest, out2.ParamsDigest); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("two runs of identical input produced different report bytes")
	}
	if out1.Summary.SHA256 != out2.Summary.SHA256 {
		t.Fatalf("checksum drifted: %s vs %s", out1.Summary.SHA256, out2.Summary.SHA256)
	}
}

func TestResumeProducesIdenticalResult(t *testing.T) {
	root, names, _ := setup(t)
	cp := filepath.Join(root, "state", "checkpoint.json")
	full, err := runner(root, cp).Run(names)
	if err != nil && !errors.Is(err, ErrDamaged) {
		t.Fatal(err)
	}
	params := runner(root, "").Params

	// Simulate a crash after the normalize stage by dropping later stages.
	store, err := loadStore(cp)
	if err != nil {
		t.Fatal(err)
	}
	if err := truncateAfter(store, cp, "normalize"); err != nil {
		t.Fatal(err)
	}
	resumed, err := runner(root, cp).Run(names)
	if err != nil && !errors.Is(err, ErrDamaged) {
		t.Fatal(err)
	}
	if len(resumed.Resumed) != 2 {
		t.Fatalf("resumed stages=%v want [ingest normalize]", resumed.Resumed)
	}
	if len(resumed.Resumed) > 0 && resumed.Resumed[0] != "ingest" {
		t.Fatalf("resume order=%v", resumed.Resumed)
	}
	var a, b bytes.Buffer
	_ = WriteJSONL(&a, full, params, full.InputDigest, full.ParamsDigest)
	_ = WriteJSONL(&b, resumed, params, resumed.InputDigest, resumed.ParamsDigest)
	// The report carries no resume provenance, so it must be byte-identical.
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("resumed report differs from a full run")
	}
	if full.Summary.SHA256 != resumed.Summary.SHA256 {
		t.Fatalf("resumed checksum %s != %s", resumed.Summary.SHA256, full.Summary.SHA256)
	}
	if full.Summary.OffsetErrorBytes != resumed.Summary.OffsetErrorBytes {
		t.Fatal("offset error changed across resume")
	}
}

func TestResumeRejectsChangedInput(t *testing.T) {
	root, names, _ := setup(t)
	cp := filepath.Join(root, "cp.json")
	if _, err := runner(root, cp).Run(names); err != nil && !errors.Is(err, ErrDamaged) {
		t.Fatal(err)
	}
	// Change one input byte; the digest must no longer match.
	path := filepath.Join(root, filepath.FromSlash(names[2]))
	if err := os.WriteFile(path, []byte("WEBVTT\n\n00:01.000 --> 00:02.000\nchanged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := runner(root, cp).Run(names)
	if !errors.Is(err, ErrCheckpoint) {
		t.Fatalf("want ErrCheckpoint for changed input, got %v", err)
	}
}

func TestVerifyReport(t *testing.T) {
	root, names, _ := setup(t)
	out, err := runner(root, "").Run(names)
	if err != nil && !errors.Is(err, ErrDamaged) {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteJSONL(&buf, out, runner(root, "").Params, out.InputDigest, out.ParamsDigest); err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseReport(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	res, digest, match := VerifyReport(parsed)
	if res.Failed != 0 {
		t.Fatalf("invariants failed: %v", res.FailedChecks())
	}
	if parsed.GatesFailed != 0 {
		t.Fatalf("stage gates failed: %d", parsed.GatesFailed)
	}
	if len(parsed.Invariants) == 0 {
		t.Fatal("report carried no invariant records")
	}
	if !match || digest != out.Summary.SHA256 {
		t.Fatalf("checksum mismatch: verify=%s run=%s match=%v", digest, out.Summary.SHA256, match)
	}
	if parsed.Summary.CorruptRanges != out.Summary.CorruptRanges {
		t.Fatalf("summary corrupt ranges=%d want %d", parsed.Summary.CorruptRanges, out.Summary.CorruptRanges)
	}
}

func TestReportRejectsUnknownRecords(t *testing.T) {
	if _, err := ParseReport(bytes.NewReader([]byte(`{"kind":"mystery"}` + "\n"))); err == nil {
		t.Fatal("want unknown kind error")
	}
	if _, err := ParseReport(bytes.NewReader([]byte(`{"kind":"run","version":1,"bogus":true}` + "\n"))); err == nil {
		t.Fatal("want unknown field error")
	}
}

func TestEveryFaultClassIsReported(t *testing.T) {
	root, names, _ := setup(t)
	out, err := runner(root, "").Run(names)
	if err != nil && !errors.Is(err, ErrDamaged) {
		t.Fatal(err)
	}
	want := []string{
		"truncated_box",    // cut mdat
		"mp4_length",       // impossible box size
		"srt_time",         // unparsable SRT cue
		"vtt_time",         // backwards VTT cue
		"truncated_cue",    // unterminated subtitle body
		"rtp_sequence_gap", // lost packets
		"rtp_wrap",         // 32-bit rollover
		"clock_alignment",  // loss breaks interpolation
		"node_drift",       // measured node skew
		"timecode_label",   // impossible drop-frame label
	}
	seen := make(map[string]int)
	for _, is := range out.Diagnose.Issues {
		seen[string(is.Category)]++
	}
	for _, cat := range want {
		if seen[cat] == 0 {
			t.Errorf("fault class %q was not reported (seen: %v)", cat, seen)
		}
	}
}

func TestTruncatedBoxInvalidatesKeyframes(t *testing.T) {
	root, names, _ := setup(t)
	out, err := runner(root, "").Run(names)
	if err != nil && !errors.Is(err, ErrDamaged) {
		t.Fatal(err)
	}
	var corrupt *index.Index
	for i := range out.Index.Indexes {
		if out.Index.Indexes[i].Asset == "video/corrupt.mp4" {
			corrupt = &out.Index.Indexes[i]
		}
	}
	if corrupt == nil {
		t.Fatal("no index for the truncated asset")
	}
	if corrupt.Untrusted == 0 {
		t.Fatal("truncation did not invalidate any keyframe")
	}
	for _, kf := range corrupt.Keyframes {
		if !kf.Trusted && kf.Span.Start < 2667 {
			t.Fatalf("keyframe %d marked untrusted but its bytes are intact", kf.Number)
		}
	}
}

func TestIllegalCueDoesNotPolluteFollowingCue(t *testing.T) {
	root, names, _ := setup(t)
	out, err := runner(root, "").Run(names)
	if err != nil && !errors.Is(err, ErrDamaged) {
		t.Fatal(err)
	}
	// broken.vtt rejects its second cue (end before start). The rejected cue
	// must not appear in the results and the next one must keep its own text.
	got := map[int][]string{}
	for _, c := range out.Ingest.Cues {
		if c.Asset != "subs/broken.vtt" {
			continue
		}
		got[c.Index] = c.Lines
		if c.End.Cmp(c.Start) <= 0 {
			t.Fatalf("rejected cue leaked into results: %+v", c)
		}
	}
	// The generator writes seven valid cues and injects one backwards cue
	// between cues 2 and 3.
	if len(got) != 7 {
		t.Fatalf("kept %d cues from broken.vtt, want 7: %v", len(got), got)
	}
	// The rejected block sits between cue-2 and cue-3. Its "backwards" text
	// must be swallowed with it, and cue-3 must keep only its own two lines.
	if lines := got[3]; len(lines) != 2 || lines[0] != "line one" || lines[1] != "line two" {
		t.Fatalf("cue following the rejected one was polluted: %v", lines)
	}
	for idx, lines := range got {
		for _, l := range lines {
			if l == "backwards" {
				t.Fatalf("cue %d absorbed the rejected cue's text", idx)
			}
		}
	}
	if lines := got[4]; len(lines) != 1 || lines[0] != "vtt cue 4" {
		t.Fatalf("cue after the rejected block absorbed text: %v", lines)
	}
}
