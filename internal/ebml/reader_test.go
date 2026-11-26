package ebml_test

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"assetcheck/internal/ebml"
	"assetcheck/internal/fixture"
	"assetcheck/internal/media"
)

func writeFixture(t *testing.T, name string, opts fixture.Options) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := fixture.WriteFile(path, opts); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func parseEvents(t *testing.T, path string) (*ebml.Document, []media.TimeEvent) {
	t.Helper()
	var events []media.TimeEvent
	doc, err := ebml.ParseFile(path, ebml.Options{
		HandleEvent: func(ev media.TimeEvent) error {
			events = append(events, ev)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return doc, events
}

func TestParseKnownLengthMultitrackFixture(t *testing.T) {
	path := writeFixture(t, "clean.mkv", fixture.DefaultOptions())
	doc, events := parseEvents(t, path)

	if len(doc.Tracks) != 5 {
		t.Fatalf("tracks = %d, want 5", len(doc.Tracks))
	}
	if doc.BlockCount != 210 {
		t.Fatalf("block count = %d, want 210", doc.BlockCount)
	}
	counts := map[int]int{}
	var audio, cues, effects int
	for _, ev := range events {
		counts[ev.TrackID]++
		switch ev.Kind {
		case media.EventAudio:
			audio++
			if got := media.ToMilliseconds(media.Sub(ev.End, ev.Start)); got != 10 {
				t.Fatalf("audio block %d duration = %.3fms, want 10", ev.BlockIndex, got)
			}
		case media.EventSubtitle:
			cues++
		case media.EventEffect:
			effects++
		}
	}
	if audio != 200 || cues != 8 || effects != 2 {
		t.Fatalf("counts audio=%d cues=%d effects=%d", audio, cues, effects)
	}
	for track, want := range map[int]int{1: 100, 2: 100, 3: 4, 4: 4, 5: 2} {
		if counts[track] != want {
			t.Fatalf("track %d blocks = %d, want %d", track, counts[track], want)
		}
	}

	// At time zero the bilingual cues precede the two interleaved PCM tracks.
	firstFour := []int{events[0].TrackID, events[1].TrackID, events[2].TrackID, events[3].TrackID}
	wantFirst := []int{3, 4, 1, 2}
	for i := range wantFirst {
		if firstFour[i] != wantFirst[i] {
			t.Fatalf("first track order = %v, want prefix %v", firstFour, wantFirst)
		}
	}
	if doc.StructureFingerprint == "" {
		t.Fatal("empty structure fingerprint")
	}
}

func TestUnknownLengthSegmentEndsAtEOF(t *testing.T) {
	opts := fixture.DefaultOptions()
	opts.UnknownSegment = true
	path := writeFixture(t, "unknown-eof.mkv", opts)
	doc, events := parseEvents(t, path)

	var segment *ebml.ElementInfo
	for i := range doc.Elements {
		if doc.Elements[i].ID == ebml.IDSegment {
			segment = &doc.Elements[i]
			break
		}
	}
	if segment == nil {
		t.Fatal("Segment element not recorded")
	}
	if !segment.Unknown || segment.Size != -1 {
		t.Fatalf("segment = %+v, want unknown size marker", segment)
	}
	if len(events) != 210 {
		t.Fatalf("events = %d, want all 210 before EOF", len(events))
	}
}

func TestIllegalVINTReportsPathAndOffset(t *testing.T) {
	opts := fixture.DefaultOptions()
	opts.InvalidVINT = true
	path := writeFixture(t, "invalid-vint.mkv", opts)
	_, err := ebml.ParseFile(path, ebml.Options{})
	if err == nil {
		t.Fatal("expected illegal VINT error")
	}
	me, ok := media.AsError(err)
	if !ok {
		t.Fatalf("error is not structured: %T %v", err, err)
	}
	if me.Code != "EBML_ILLEGAL_VINT" {
		t.Fatalf("code = %q, want EBML_ILLEGAL_VINT", me.Code)
	}
	if me.Offset < 0 {
		t.Fatalf("offset = %d, want non-negative byte offset", me.Offset)
	}
	if !strings.Contains(me.ElementPath, "/Segment/Cluster/SimpleBlock") {
		t.Fatalf("element path = %q, want cluster/simple block path", me.ElementPath)
	}
	if code := media.ExitCode(err); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}

func TestTruncatedClusterReportsLocation(t *testing.T) {
	path := writeFixture(t, "truncated.mkv", fixture.DefaultOptions())
	if err := fixture.TruncateCluster(path); err != nil {
		t.Fatal(err)
	}
	_, err := ebml.ParseFile(path, ebml.Options{})
	if err == nil {
		t.Fatal("expected truncation error")
	}
	me, ok := media.AsError(err)
	if !ok {
		t.Fatalf("error is not structured: %v", err)
	}
	if media.ExitCode(err) != 2 {
		t.Fatalf("exit code for %q = %d, want 2", me.Code, media.ExitCode(err))
	}
	if me.Offset < 0 {
		t.Fatal("truncation error lacks offset")
	}
	if !strings.Contains(me.ElementPath, "Cluster") {
		t.Fatalf("path = %q, want Cluster location", me.ElementPath)
	}
}

func TestUnknownLengthInsideKnownParentEndsAtParentBoundary(t *testing.T) {
	unk, err := ebml.EncodeUnknownSize(1)
	if err != nil {
		t.Fatal(err)
	}
	writingSize, err := ebml.EncodeSize(2)
	if err != nil {
		t.Fatal(err)
	}
	var info []byte
	info = append(info, ebml.EncodeID(ebml.IDInfo)...)
	info = append(info, unk...)
	info = append(info, ebml.EncodeID(ebml.IDWritingApp)...)
	info = append(info, writingSize...)
	info = append(info, []byte("ok")...)

	segSize, err := ebml.EncodeSize(uint64(len(info)))
	if err != nil {
		t.Fatal(err)
	}
	var segment []byte
	segment = append(segment, ebml.EncodeID(ebml.IDSegment)...)
	segment = append(segment, segSize...)
	segment = append(segment, info...)

	doc, err := ebml.Parse(bytes.NewReader(segment), ebml.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, el := range doc.Elements {
		if el.ID == ebml.IDInfo {
			found = true
			if !el.Unknown || el.Size != -1 {
				t.Fatalf("Info = %+v, want unknown length", el)
			}
		}
	}
	if !found {
		t.Fatal("Info element not recorded")
	}
}

func TestTruncatedHeaderAndLengthOutOfBoundsAreLocated(t *testing.T) {
	size200, err := ebml.EncodeSize(200)
	if err != nil {
		t.Fatal(err)
	}
	truncated := append(ebml.EncodeID(ebml.IDSegment), size200[:1]...)
	_, err = ebml.Parse(bytes.NewReader(truncated), ebml.Options{})
	me, ok := media.AsError(err)
	if !ok || me.Offset != int64(len(ebml.EncodeID(ebml.IDSegment))) {
		t.Fatalf("truncated header error = %v, want structured offset at size VINT", err)
	}
	if !strings.Contains(me.ElementPath, "Segment") || media.ExitCode(err) != 2 {
		t.Fatalf("truncated header location/path wrong: %+v", me)
	}

	badSize, err := ebml.EncodeSize(1000)
	if err != nil {
		t.Fatal(err)
	}
	var infoHeader []byte
	infoHeader = append(infoHeader, ebml.EncodeID(ebml.IDInfo)...)
	infoHeader = append(infoHeader, badSize...)
	segSize, err := ebml.EncodeSize(uint64(len(infoHeader)))
	if err != nil {
		t.Fatal(err)
	}
	var outOfBounds []byte
	outOfBounds = append(outOfBounds, ebml.EncodeID(ebml.IDSegment)...)
	outOfBounds = append(outOfBounds, segSize...)
	outOfBounds = append(outOfBounds, infoHeader...)
	_, err = ebml.Parse(bytes.NewReader(outOfBounds), ebml.Options{})
	me, ok = media.AsError(err)
	if !ok || me.Code != "EBML_LENGTH_OUT_OF_BOUNDS" {
		t.Fatalf("out-of-bounds error = %v", err)
	}
	if !strings.Contains(me.ElementPath, "/Segment/Info") || me.Offset < 0 {
		t.Fatalf("out-of-bounds location wrong: %+v", me)
	}
}

func TestVINTSizeRoundTripAndReservedUnknown(t *testing.T) {
	for _, size := range []uint64{0, 1, 126, 127, 16382, 16383, 1_000_000} {
		enc, err := ebml.EncodeSize(size)
		if err != nil {
			t.Fatal(err)
		}
		r := ebml.NewCountingReader(bytes.NewReader(enc), 100)
		v, err := ebml.ReadVINT(r)
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		if v.Unknown || v.Value != size || v.Offset != 100 {
			t.Fatalf("size %d decoded = %+v", size, v)
		}
	}
	unk, err := ebml.EncodeUnknownSize(2)
	if err != nil {
		t.Fatal(err)
	}
	r2 := ebml.NewCountingReader(bytes.NewReader(unk), 0)
	v, err := ebml.ReadVINT(r2)
	if err != nil || !v.Unknown {
		t.Fatalf("unknown VINT = %+v err=%v", v, err)
	}
}
