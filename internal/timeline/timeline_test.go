package timeline

import (
	"encoding/binary"
	"os"
	"strings"
	"testing"

	"rtpregress/internal/fixture"
	"rtpregress/internal/media"
	"rtpregress/internal/planner"
	"rtpregress/internal/rtp"
)

// generateFixtures writes the self-generated sample and returns its layout.
func generateFixtures(t *testing.T) *fixture.Layout {
	t.Helper()
	lay, err := fixture.Generate(t.TempDir())
	if err != nil {
		t.Fatalf("generate fixtures: %v", err)
	}
	return lay
}

func TestParseSampleEvents(t *testing.T) {
	lay := generateFixtures(t)
	res, err := ParseMP4File(lay.SampleMP4)
	if err != nil {
		t.Fatalf("parse sample: %v", err)
	}
	bySource := map[Source]int{}
	times := map[Source][]int64{}
	for _, e := range res.Events {
		bySource[e.Source]++
		times[e.Source] = append(times[e.Source], e.TimeNs)
	}
	want := map[Source]int{
		SourceSubtitle: 2, SourceChapter: 2, SourceID3: 2, SourceEmsg: 2,
	}
	for src, n := range want {
		if bySource[src] != n {
			t.Fatalf("%s events = %d, want %d (all: %+v)", src, bySource[src], n, bySource)
		}
	}
	// Subtitle samples start at 0 and 600ms on a 1000-tick clock.
	if got := times[SourceSubtitle]; len(got) != 2 || got[0] != 0 || got[1] != 600_000_000 {
		t.Fatalf("subtitle times = %v", got)
	}
	// ID3 runs on a 48kHz clock: 9600 ticks is 200ms.
	if got := times[SourceID3]; len(got) != 2 || got[0] != 0 || got[1] != 200_000_000 {
		t.Fatalf("id3 times = %v", got)
	}
	// emsg presentation deltas are 900ms and 1800ms.
	if got := times[SourceEmsg]; len(got) != 2 || got[0] != 900_000_000 || got[1] != 1_800_000_000 {
		t.Fatalf("emsg times = %v", got)
	}
	if res.MVHDTimescale != 1000 {
		t.Fatalf("mvhd timescale = %d", res.MVHDTimescale)
	}
	if len(res.Tracks) != 3 {
		t.Fatalf("tracks = %d, want 3", len(res.Tracks))
	}
	for _, tr := range res.Tracks {
		if err := tr.Timebase.Validate(); err != nil {
			t.Fatalf("track %s timebase invalid: %v", tr.Name, err)
		}
	}
}

func TestParseTruncatedReportsTrackAndOffset(t *testing.T) {
	lay := generateFixtures(t)
	_, err := ParseMP4File(lay.Truncated)
	if err == nil {
		t.Fatal("truncated box was accepted")
	}
	if media.KindOf(err) != media.KindInput {
		t.Fatalf("kind = %s, want input", media.KindOf(err))
	}
	loc, ok := media.LocOf(err)
	if !ok || loc.Track == "" || !loc.HasOffset {
		t.Fatalf("error must name track and byte offset: %+v", loc)
	}
	// The final (cut) emsg box begins well inside the file.
	if loc.Offset <= 0 {
		t.Fatalf("offset = %d, want the truncated box position", loc.Offset)
	}
}

func TestParseRejectsInvalidBoxLength(t *testing.T) {
	// A box declaring a size smaller than its own 8-byte header is invalid.
	data := []byte{
		0, 0, 0, 4, 'f', 'r', 'e', 'e', // size 4 < header 8
	}
	_, err := ParseMP4(data, "bad.mp4")
	if err == nil {
		t.Fatal("invalid box length was accepted")
	}
	if loc, ok := media.LocOf(err); !ok || !loc.HasOffset {
		t.Fatalf("invalid length must report a byte offset: %+v", loc)
	}
}

func TestParseRejectsZeroTimescaleEmsg(t *testing.T) {
	// ftyp | empty moov | emsg with a zero timescale: the timebase is
	// unusable and the event must be refused with its box offset.
	data := buildBox("ftyp", nil)
	data = append(data, buildBox("moov", nil)...)
	body := []byte{}
	body = append(body, 0)                      // scheme id terminator
	body = append(body, 0)                      // value terminator
	body = append(body, 0, 0, 0, 0)             // timescale = 0
	body = append(body, 0, 0, 0, 0)             // presentation_time_delta
	body = append(body, 0, 0, 0, 0)             // event_duration
	body = append(body, 0, 0, 0, 1)             // id
	full := append([]byte{0, 0, 0, 0}, body...) // version+flags
	data = append(data, buildFullBox("emsg", full)...)

	_, err := ParseMP4(data, "zero-emsg.mp4")
	if err == nil {
		t.Fatal("zero-timescale emsg was accepted")
	}
	if media.KindOf(err) != media.KindInput {
		t.Fatalf("kind = %s, want input", media.KindOf(err))
	}
	loc, ok := media.LocOf(err)
	if !ok || !loc.HasOffset {
		t.Fatalf("zero timescale must report a byte offset: %+v", loc)
	}
}

func buildBox(typ string, body []byte) []byte {
	out := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(body)))
	copy(out[4:8], typ)
	return append(out, body...)
}

func buildFullBox(typ string, body []byte) []byte {
	out := make([]byte, 12, 12+len(body))
	binary.BigEndian.PutUint32(out[0:4], uint32(12+len(body)))
	copy(out[4:8], typ)
	return append(out, body...)
}

func TestExtractSEI(t *testing.T) {
	lay := generateFixtures(t)
	f, err := os.Open(lay.Trace)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tr, err := rtp.ParseTrace(f)
	if err != nil {
		t.Fatal(err)
	}
	re, err := rtp.Reorder(tr, rtp.ReorderOptions{Window: 4, Stage: rtp.StageAlign})
	if err != nil {
		t.Fatal(err)
	}
	events, err := ExtractSEI(tr, re)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("SEI events = %d, want 2", len(events))
	}
	// RTP timestamps, not the SEI body, define the time: 27000/90000 = 300ms.
	if events[0].TimeNs != 300_000_000 || events[0].Text != "sei-cue-1" {
		t.Fatalf("first SEI = %+v", events[0])
	}
	if events[1].TimeNs != 600_000_000 || events[1].Text != "sei-cue-2" {
		t.Fatalf("second SEI = %+v", events[1])
	}
	for _, e := range events {
		if !e.HasOffset || e.Track != "video" {
			t.Fatalf("SEI event missing provenance: %+v", e)
		}
	}
}

func TestAlignQuantizesAndSegments(t *testing.T) {
	plan := &planner.Plan{
		Candidate: planner.Candidate{PrecisionUS: 10, SegmentMS: 1000},
		Segments:  2,
	}
	events := []Event{
		{Source: SourceSubtitle, Track: "subtitle", TimeNs: 12_000},
		{Source: SourceChapter, Track: "chapter", TimeNs: 1_500_000_000},
		{Source: SourceEmsg, Track: "meta", TimeNs: 999_995_000},
	}
	al, err := Align(events, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(al.Events) != 3 {
		t.Fatalf("aligned = %d", len(al.Events))
	}
	// 12us snaps to 10us (2us error); 1.5s is already on the grid.
	if got := al.Events[0].QuantizedNs; got != 10_000 {
		t.Fatalf("quantized = %d, want 10000", got)
	}
	if al.MaxDeviationNs > plan.QuantizeDeviationNs() {
		t.Fatalf("deviation %d exceeds half-precision %d",
			al.MaxDeviationNs, plan.QuantizeDeviationNs())
	}
	// 999.995ms rounds up to 1.000s, landing in segment 1.
	if seg := al.Events[2].Segment; seg != 1 {
		t.Fatalf("event at ~1s landed in segment %d, want 1", seg)
	}
	if al.Segments != 2 || len(al.SegmentCounts) != 2 {
		t.Fatalf("segments = %d counts = %v", al.Segments, al.SegmentCounts)
	}
	var total int
	for _, c := range al.SegmentCounts {
		total += c
	}
	if total != len(al.Events) {
		t.Fatalf("segment counts sum to %d, want %d", total, len(al.Events))
	}
}

func TestAlignRejectsNilPlan(t *testing.T) {
	_, err := Align([]Event{{TimeNs: 0}}, nil)
	if err == nil || media.KindOf(err) != media.KindBudget {
		t.Fatalf("nil plan: %v", err)
	}
}

func TestSortEventsDeterministic(t *testing.T) {
	events := []Event{
		{Source: SourceSEI, Track: "video", TimeNs: 100, Text: "b"},
		{Source: SourceSEI, Track: "video", TimeNs: 100, Text: "a"},
		{Source: SourceChapter, Track: "chapter", TimeNs: 50},
	}
	SortEvents(events)
	if events[0].TimeNs != 50 {
		t.Fatalf("earliest event not first: %+v", events[0])
	}
	if events[1].Text != "a" || events[2].Text != "b" {
		t.Fatalf("tie-break order unstable: %q %q", events[1].Text, events[2].Text)
	}
}

func TestSummarize(t *testing.T) {
	events := []Event{
		{Source: SourceSubtitle, TimeNs: 0, DurationNs: 100},
		{Source: SourceSEI, TimeNs: 50, DurationNs: 200},
	}
	s := Summarize(events)
	if s.Count != 2 || s.BySource["subtitle"] != 1 || s.BySource["sei"] != 1 {
		t.Fatalf("summary = %+v", s)
	}
	if s.MaxTimeNs != 250 {
		t.Fatalf("max time = %d, want 250", s.MaxTimeNs)
	}
	got := strings.Join(s.SourcesSorted(), ",")
	if got != "sei,subtitle" {
		t.Fatalf("sorted sources = %s", got)
	}
}
