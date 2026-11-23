package media

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func remuxSetup(t *testing.T) (*Probe, *TrackMap, []byte) {
	t.Helper()
	fx, err := BuildFixture(FixtureOptions{
		Seed: 41,
		Tracks: []FixtureTrack{
			{ID: 1, Codec: "avc1", Lang: "eng", Timescale: 90000, Samples: 24, SyncEvery: 3, Width: 1280, Height: 720},
			{ID: 2, Codec: "mp4a", Lang: "eng", Timescale: 48000, Samples: 24},
		},
	})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	p, err := ProbeFile(bytes.NewReader(fx.Bytes), int64(len(fx.Bytes)), "source.mov")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	m, err := BuildTrackMap(p, demoTargetsTest)
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	return p, m, fx.Bytes
}

var demoTargetsTest = []TrackTarget{
	{Kind: KindVideo, Required: true},
	{Kind: KindAudio, Required: true},
}

func TestRemuxLosslessRoundtrip(t *testing.T) {
	p, m, raw := remuxSetup(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "remux.mp4")
	res, err := Remux(bytes.NewReader(raw), p, m, out, RemuxOptions{OutDir: dir})
	if err != nil {
		t.Fatalf("Remux: %v", err)
	}
	if res.SampleCount != 48 {
		t.Errorf("sample count = %d, want 48", res.SampleCount)
	}
	for _, ev := range res.Tracks {
		if !ev.ByteMatch || !ev.DTSMatch || !ev.CTSMatch || !ev.SyncMatch {
			t.Errorf("track %d mismatch: %+v", ev.TargetID, ev)
		}
		if ev.SourceSHA != ev.OutputSHA {
			t.Errorf("track %d sha %s != %s", ev.TargetID, ev.SourceSHA, ev.OutputSHA)
		}
	}
	p2, size, err := ReprobeFile(out)
	if err != nil {
		t.Fatalf("reprobe output: %v", err)
	}
	if int64(len(raw)) == size {
		t.Logf("output size equals source size (%d); layout may be preserved", size)
	}
	if len(p2.Tracks) != 2 {
		t.Fatalf("output tracks = %d", len(p2.Tracks))
	}
	// target ids are assigned in layout order, independent of source ids
	for i, wantID := range []uint32{1, 2} {
		if p2.Tracks[i].TrackID != wantID {
			t.Errorf("target track %d id = %d, want %d", i, p2.Tracks[i].TrackID, wantID)
		}
		if p2.Tracks[i].SampleCount != p.Tracks[i].SampleCount {
			t.Errorf("target track %d samples = %d, want %d", i, p2.Tracks[i].SampleCount, p.Tracks[i].SampleCount)
		}
	}
	// no temp files may remain
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("leftover temp file %q", e.Name())
		}
	}
}

func TestRemuxReordersTracksByMap(t *testing.T) {
	// Map audio before video and verify the output track order follows it.
	fx, err := BuildFixture(FixtureOptions{
		Seed: 5,
		Tracks: []FixtureTrack{
			{ID: 7, Codec: "avc1", Lang: "eng", Timescale: 90000, Samples: 6, SyncEvery: 3},
			{ID: 9, Codec: "mp4a", Lang: "eng", Timescale: 48000, Samples: 6},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := ProbeFile(bytes.NewReader(fx.Bytes), int64(len(fx.Bytes)), "src.mp4")
	if err != nil {
		t.Fatal(err)
	}
	m, err := BuildTrackMap(p, []TrackTarget{{Kind: KindAudio, Required: true}, {Kind: KindVideo, Required: true}})
	if err != nil {
		t.Fatal(err)
	}
	if m.Entries[0].SourceID != 9 || m.Entries[0].TargetID != 1 {
		t.Fatalf("map order = %+v", m.Entries)
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "reordered.mp4")
	if _, err := Remux(bytes.NewReader(fx.Bytes), p, m, out, RemuxOptions{OutDir: dir}); err != nil {
		t.Fatal(err)
	}
	p2, _, err := ReprobeFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if p2.Tracks[0].Codec != "mp4a" || p2.Tracks[1].Codec != "avc1" {
		t.Errorf("output order = %s,%s; want mp4a,avc1", p2.Tracks[0].Codec, p2.Tracks[1].Codec)
	}
}

func TestRemuxRejectsEscapingPath(t *testing.T) {
	p, m, raw := remuxSetup(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "..", "escape.mp4")
	_, err := Remux(bytes.NewReader(raw), p, m, out, RemuxOptions{OutDir: dir})
	if err == nil {
		t.Fatal("path escape accepted")
	}
	if ClassOf(err) != ClassInvalidArgument {
		t.Errorf("class = %s, want invalid_argument", ClassOf(err))
	}
	if _, statErr := os.Stat(filepath.Join(dir, "..", "escape.mp4")); statErr == nil {
		t.Fatal("escaping output file was created")
	}
}

func TestRemuxCleansTempOnFailure(t *testing.T) {
	p, m, raw := remuxSetup(t)
	// Corrupt the sample data so verification/copy fails after the moov is
	// written; the temp file must be removed and the input left untouched.
	bad := append([]byte(nil), raw...)
	mdat, err := findBoxOffset(bad, "mdat")
	if err != nil {
		t.Fatal(err)
	}
	if mdat+32 < int64(len(bad)) {
		bad[mdat+32] ^= 0xff
	}
	// A byte flip alone is still valid content, so force a structural failure
	// by shrinking the file instead.
	bad = bad[:len(bad)-10]
	dir := t.TempDir()
	out := filepath.Join(dir, "failed.mp4")
	_, err = Remux(bytes.NewReader(bad), p, m, out, RemuxOptions{OutDir: dir})
	if err == nil {
		t.Fatal("truncated input accepted")
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Fatal("failed remux left a visible output file")
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("failed remux left temp file %q", e.Name())
		}
	}
}

func TestRemuxOverwritesAtomically(t *testing.T) {
	p, m, raw := remuxSetup(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "out.mp4")
	if err := os.WriteFile(out, []byte("stale content that must be replaced"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Remux(bytes.NewReader(raw), p, m, out, RemuxOptions{OutDir: dir}); err != nil {
		t.Fatal(err)
	}
	p2, _, err := ReprobeFile(out)
	if err != nil {
		t.Fatalf("output was not replaced with a valid file: %v", err)
	}
	if len(p2.Tracks) != 2 {
		t.Errorf("output tracks = %d", len(p2.Tracks))
	}
}

func ReprobeFile(path string) (*Probe, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	p, err := ProbeFile(f, fi.Size(), path)
	return p, fi.Size(), err
}

func TestTrackMapConflicts(t *testing.T) {
	fx, err := BuildFixture(FixtureOptions{
		Seed: 3,
		Tracks: []FixtureTrack{
			{ID: 1, Codec: "avc1", Lang: "eng", Timescale: 90000, Samples: 4, SyncEvery: 2},
			{ID: 2, Codec: "avc1", Lang: "eng", Timescale: 90000, Samples: 4, SyncEvery: 2},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := ProbeFile(bytes.NewReader(fx.Bytes), int64(len(fx.Bytes)), "dup.mp4")
	if err != nil {
		t.Fatal(err)
	}
	// Two video tracks and one video target: ambiguous match.
	if _, err := BuildTrackMap(p, []TrackTarget{{Kind: KindVideo, Required: true}}); ClassOf(err) != ClassMappingConflict {
		t.Errorf("ambiguous match class = %s, want mapping_conflict", ClassOf(err))
	}
	// A required audio target has no source: explicit missing_track.
	if _, err := BuildTrackMap(p, []TrackTarget{{Kind: KindVideo, Required: true}, {Kind: KindAudio, Required: true}}); ClassOf(err) != ClassMappingConflict && ClassOf(err) != ClassMissingTrack {
		t.Errorf("missing audio class = %s", ClassOf(err))
	}
	// Disambiguate by source id.
	m, err := BuildTrackMap(p, []TrackTarget{{Kind: KindVideo, SourceID: 2, Required: true}})
	if err != nil {
		t.Fatalf("explicit source id: %v", err)
	}
	if m.Entries[0].SourceID != 2 {
		t.Errorf("source id = %d, want 2", m.Entries[0].SourceID)
	}
	// An unruly zero timescale is rejected as a timebase error.
	p.Timescale = 0
	if _, err := BuildTrackMap(p, []TrackTarget{{Kind: KindVideo, SourceID: 1}}); ClassOf(err) != ClassTimebase {
		t.Errorf("timebase class = %s, want timebase", ClassOf(err))
	}
}

func TestTimebaseOverflowAndRounding(t *testing.T) {
	if _, err := Rescale(1<<63, 1, 4); err != ErrTimestampOverflow {
		t.Errorf("Rescale overflow = %v", err)
	}
	if _, err := Rescale(1, 0, 1); err != ErrInvalidTimescale {
		t.Errorf("zero src = %v", err)
	}
	got, err := RescaleRound(1, 3, 2) // 1/3s in halves -> round(0.666..) = 1
	if err != nil || got != 1 {
		t.Errorf("RescaleRound(1,3,2) = %d, %v", got, err)
	}
	tb := Timebase{Timescale: 90000}
	ns, err := tb.Nanoseconds(90000)
	if err != nil || ns != 1_000_000_000 {
		t.Errorf("Nanoseconds = %d, %v", ns, err)
	}
	back, err := tb.FromNanoseconds(ns)
	if err != nil || back != 90000 {
		t.Errorf("FromNanoseconds roundtrip = %d, %v", back, err)
	}
}

func TestErrorContextFormatting(t *testing.T) {
	err := NewError(ClassInvalidSample, "probe", 1234, "bad sample").WithTrack(7).WithSegment(3).WithPacket(99)
	want := "invalid_sample: probe at offset 1234 track=7 segment=3 packet=99: bad sample"
	if err.Error() != want {
		t.Errorf("error string = %q, want %q", err.Error(), want)
	}
	if !IsClass(err, ClassInvalidSample) {
		t.Error("IsClass failed")
	}
	if ClassOf(os.ErrNotExist) != ClassIO {
		t.Error("unclassified error should map to io")
	}
}
