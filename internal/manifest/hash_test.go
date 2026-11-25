package manifest

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"archive.example/mediakernel/internal/audio"
	"archive.example/mediakernel/internal/events"
	"archive.example/mediakernel/internal/render"
)

func sampleInputs() Inputs {
	frames := render.Sequence{
		StartNS:         0,
		EndNS:           200_000_000,
		FrameDurationNS: 40_000_000,
		Frames: []render.Frame{
			{Index: 0, AtNS: 0, MeanByte: 1.0, MinByte: 0, MaxByte: 10, SHA256: "aa"},
			{Index: 1, AtNS: 40_000_000, MeanByte: 2.0, MinByte: 1, MaxByte: 20, SHA256: "bb"},
		},
		MaxAlignError: 40_000_000,
	}
	committed := []events.Event{
		{Track: events.TrackSubtitle, Kind: events.KindSubtitle, At: 0, Seq: 1, Text: "x"},
	}
	return Inputs{
		Fixture: "normal",
		Seed:    17,
		Timeline: TimelineLike{
			WatermarkNS: 100_000_000,
			Committed:   committed,
			ByTrack:     map[string]int{"subtitle": 1},
			Admitted:    1,
		},
		Frames:      frames,
		Audio:       audio.Stats{Samples: 96000, SampleRate: 48000, Channels: 2, BitsPerSample: 16, Peak: 0.85, Finite: true, SHA256: "cc"},
		RTP:         RTPSummary{Received: 10, Delivered: 10, Status: "ordered", Packets: 10},
		StateHash:   "deadbeef",
		FrameWidth:  16,
		FrameHeight: 9,
		SegmentSize: 8,
	}
}

func TestManifestBytesDeterministic(t *testing.T) {
	m := Build(sampleInputs())
	one, err := m.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	m2 := Build(sampleInputs())
	two, err := m2.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(one, two) {
		t.Fatal("manifest bytes are not deterministic")
	}
}

func TestManifestHashRecordedAndStable(t *testing.T) {
	m := Build(sampleInputs())
	body, err := m.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if m.ManifestSHA256 == "" {
		t.Fatal("manifest digest missing")
	}
	var decoded Manifest
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ManifestSHA256 != m.ManifestSHA256 {
		t.Fatalf("digest mismatch on round-trip: %s vs %s", decoded.ManifestSHA256, m.ManifestSHA256)
	}
}

func TestManifestClockAndPathIndependent(t *testing.T) {
	// Same inputs, different working directory: bytes must not change.
	dir := t.TempDir()
	sub := filepath.Join(dir, "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	old, _ := os.Getwd()
	if err := os.Chdir(sub); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(old) }()
	m := Build(sampleInputs())
	body, err := m.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), dir) {
		t.Fatal("manifest leaks the working directory")
	}
}

func TestManifestGapsSortedAndUnresolved(t *testing.T) {
	in := sampleInputs()
	in.Gaps = []events.Gap{
		{Track: events.TrackRTP, Seq: 3, At: -1, Reason: "watermark_passed"},
		{Track: events.TrackRTP, Seq: 1, At: -1, Reason: "watermark_passed"},
		{Track: events.TrackRTP, Seq: 2, At: -1, Reason: "watermark_passed", Filled: true},
	}
	m := Build(in)
	if len(m.Gaps) != 2 {
		t.Fatalf("gaps %d, want 2 unresolved", len(m.Gaps))
	}
	if m.Gaps[0].Seq != 1 || m.Gaps[1].Seq != 3 {
		t.Fatalf("gaps not sorted: %+v", m.Gaps)
	}
}

func TestManifestErrorsSortedByOffset(t *testing.T) {
	in := sampleInputs()
	in.Errors = []*events.Error{
		events.NewError(events.CodeTruncated, events.TrackSEI, 0, 900, "truncated", "b", nil),
		events.NewError(events.CodeTruncated, events.TrackSubtitle, 0, 200, "truncated", "a", nil),
	}
	m := Build(in)
	if len(m.Errors) != 2 {
		t.Fatalf("errors %d", len(m.Errors))
	}
	if m.Errors[0].Offset != 200 || m.Errors[1].Offset != 900 {
		t.Fatalf("errors not sorted: %+v", m.Errors)
	}
}

func TestWriteAtomicAndReadBack(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteAtomic(dir, "manifest.json", []byte("hello\n"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello\n" {
		t.Fatalf("content %q", got)
	}
	// No temporary files remain.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestWriteAtomicRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	if _, err := WriteAtomic(dir, "../escape.json", []byte("x")); err == nil {
		t.Fatal("path traversal accepted")
	}
	if _, err := WriteAtomic(dir, "a/b.json", []byte("x")); err == nil {
		t.Fatal("nested path accepted")
	}
}

func TestResolveOutDirRejectsAmbiguousPaths(t *testing.T) {
	if _, err := ResolveOutDir(""); err == nil {
		t.Fatal("empty accepted")
	}
	if _, err := ResolveOutDir("/tmp/../etc"); err == nil {
		t.Fatal("parent traversal accepted")
	}
	if _, err := ResolveOutDir("~/archive"); err == nil {
		t.Fatal("tilde accepted")
	}
	abs, err := ResolveOutDir("/tmp/archive-x")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(abs) {
		t.Fatalf("not absolute: %s", abs)
	}
}

func TestAlignmentFlag(t *testing.T) {
	in := sampleInputs()
	in.Frames.MaxAlignError = 10
	m := Build(in)
	if !m.Alignment.AlignmentWithinFrame {
		t.Fatal("alignment should be within a frame")
	}
	in.Frames.MaxAlignError = in.Frames.FrameDurationNS + 1
	m = Build(in)
	if m.Alignment.AlignmentWithinFrame {
		t.Fatal("over-frame alignment reported as within")
	}
}

func TestManifestRoundTrip(t *testing.T) {
	m := Build(sampleInputs())
	body, err := m.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	var decoded Manifest
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Version != "archive-manifest-v1" {
		t.Fatalf("version %s", decoded.Version)
	}
	if decoded.Frames.Count != 2 || decoded.Timeline.Committed != 1 {
		t.Fatalf("round trip lost data: %+v", decoded)
	}
}
