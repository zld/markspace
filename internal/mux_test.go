package continuity

import (
	"bytes"
	"strings"
	"testing"

	"github.com/abema/go-mp4"
)

func buildProgram(t *testing.T, count int) (*Program, *TrackMap) {
	t.Helper()
	init, err := ParseInit("init.mp4", testInit())
	if err != nil {
		t.Fatal(err)
	}
	tm, err := BuildTrackMap([]TrackSource{{Label: "main", Init: init}})
	if err != nil {
		t.Fatal(err)
	}
	tl, err := NewTimeline(tm, DefaultTimelineConfig())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		v := sampleAt(i, Ticks(i*3000), 3000)
		v.Data = pattern(byte(i+1), 16)
		if err := tl.Add("main", v); err != nil {
			t.Fatal(err)
		}
		a := Sample{
			TrackKey: "audio-mp4a", SourceID: 2, Segment: "a.m4s", File: "a.m4s", Index: i,
			DTS: Ticks(i * 1920), PTS: Ticks(i * 1920), Duration: 1920,
			ByteOffset: int64(i * 8), ByteLength: 8, Data: pattern(byte(50+i), 8),
		}
		if err := tl.Add("main", a); err != nil {
			t.Fatal(err)
		}
	}
	prog, err := tl.Recompute()
	if err != nil {
		t.Fatal(err)
	}
	return prog, tm
}

func TestMuxProgramLosslessRoundTrip(t *testing.T) {
	prog, _ := buildProgram(t, 4)
	out, err := MuxProgram(prog, OutputSegmentTicks)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Init) != 2 || len(out.HLS) != 2 || len(out.DASH) == 0 {
		t.Fatalf("output shape: init=%d hls=%d dash=%d", len(out.Init), len(out.HLS), len(out.DASH))
	}
	videoSegs := out.Segments["video-avc1"]
	if len(videoSegs) == 0 {
		t.Fatal("no video fragments")
	}
	// Parse the remuxed init+segments back and compare payloads byte for byte.
	init, err := ParseInit("out/init.mp4", out.Init["video-avc1"])
	if err != nil {
		t.Fatalf("remuxed init: %v", err)
	}
	if len(init.Tracks) != 1 || init.Tracks[0].Key != "video-avc1" {
		t.Fatalf("remuxed tracks=%+v", init.Tracks)
	}
	var got [][]byte
	for i, seg := range videoSegs {
		parsed, err := ParseSegment(init, SegmentName("video-avc1", i), seg)
		if err != nil {
			t.Fatalf("remuxed segment %d: %v", i, err)
		}
		for _, s := range parsed.Samples {
			got = append(got, s.Data)
		}
	}
	var want [][]byte
	for _, s := range prog.Samples {
		if s.Target == "video-avc1" {
			want = append(want, s.Data)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("sample count: got %d want %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("sample %d bytes changed in remux", i)
		}
	}
}

func TestMuxTimingExactAndDeterministic(t *testing.T) {
	prog, _ := buildProgram(t, 5)
	a, err := MuxProgram(prog, OutputSegmentTicks)
	if err != nil {
		t.Fatal(err)
	}
	b, err := MuxProgram(prog, OutputSegmentTicks)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.DigestInput(), b.DigestInput()) {
		t.Fatal("identical program must remux to identical bytes")
	}
	// Verify remuxed timing matches the program exactly.
	init, err := ParseInit("init.mp4", a.Init["video-avc1"])
	if err != nil {
		t.Fatal(err)
	}
	var want []PlacedSample
	for _, s := range prog.Samples {
		if s.Target == "video-avc1" {
			want = append(want, s)
		}
	}
	var got []Sample
	for i, seg := range a.Segments["video-avc1"] {
		parsed, err := ParseSegment(init, SegmentName("video-avc1", i), seg)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, parsed.Samples...)
	}
	if len(got) != len(want) {
		t.Fatalf("remuxed %d samples want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].DTS != want[i].DTS || got[i].PTS != want[i].PTS || got[i].Duration != want[i].Duration {
			t.Fatalf("sample %d timing changed: got %d/%d/%d want %d/%d/%d", i,
				got[i].DTS, got[i].PTS, got[i].Duration, want[i].DTS, want[i].PTS, want[i].Duration)
		}
	}
}

func TestMuxRejectsInexactTiming(t *testing.T) {
	prog, _ := buildProgram(t, 1)
	// A 1-tick (90 kHz) sample cannot be expressed in a 48 kHz track.
	for i := range prog.Samples {
		if prog.Samples[i].Target == "audio-mp4a" {
			prog.Samples[i].Duration = 1
		}
	}
	_, err := MuxProgram(prog, OutputSegmentTicks)
	if err == nil || !IsKind(err, KindMux) {
		t.Fatalf("err=%v", err)
	}
}

func TestHLSPlaylistMarksGaps(t *testing.T) {
	starts := []Ticks{0, 180000, 540000}
	durs := []Ticks{180000, 180000, 180000}
	pl := string(HLSPlaylist("video-avc1", starts, durs, OutputSegmentTicks))
	if strings.Count(pl, "#EXT-X-DISCONTINUITY") != 1 {
		t.Fatalf("playlist should mark the single gap:\n%s", pl)
	}
	if !strings.Contains(pl, "#EXT-X-MAP:URI=\"video-avc1/init.mp4\"") {
		t.Fatalf("missing map: %s", pl)
	}
	if !strings.Contains(pl, "#EXTINF:2.000,") {
		t.Fatalf("missing extinf: %s", pl)
	}
}

func TestDASHManifestRoundTrip(t *testing.T) {
	prog, _ := buildProgram(t, 3)
	out, err := MuxProgram(prog, OutputSegmentTicks)
	if err != nil {
		t.Fatal(err)
	}
	m, err := ParseDASH("out/index.mpd", out.DASH)
	if err != nil {
		t.Fatalf("output MPD must parse: %v\n%s", err, out.DASH)
	}
	if len(m.Representations) != 2 {
		t.Fatalf("reps=%d", len(m.Representations))
	}
	for _, rep := range m.Representations {
		if len(rep.Segments) != len(out.Segments[rep.ID]) {
			t.Fatalf("rep %s: manifest segments=%d media segments=%d", rep.ID, len(rep.Segments), len(out.Segments[rep.ID]))
		}
	}
}

func TestMuxOutputParsesWithGoMP4(t *testing.T) {
	prog, _ := buildProgram(t, 2)
	out, err := MuxProgram(prog, OutputSegmentTicks)
	if err != nil {
		t.Fatal(err)
	}
	for key, segs := range out.Segments {
		for _, seg := range segs {
			if _, err := mp4.Probe(bytes.NewReader(seg)); err != nil {
				t.Fatalf("%s: go-mp4 probe: %v", key, err)
			}
		}
	}
}
