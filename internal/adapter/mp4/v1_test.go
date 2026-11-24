package mp4_test

import (
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/adapter"
	_ "mediad/internal/adapter/mp4"
	"mediad/internal/fixture"
	"mediad/internal/model"
)

func writeSeg(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMP4HappyPath(t *testing.T) {
	frames := []fixture.FrameSpec{
		{Payload: []byte("key-0"), Duration: 1000, KeyFrame: true},
		{Payload: []byte("p-1"), Duration: 1000, KeyFrame: false},
		{Payload: []byte("p-2"), Duration: 1000, KeyFrame: false},
		{Payload: []byte("key-3"), Duration: 1000, KeyFrame: true},
	}
	seg := fixture.BuildSegment(1000, 0, 1, 0, frames)
	dir := t.TempDir()
	p := filepath.Join(dir, "1.m4s")
	writeSeg(t, p, seg)

	tr, err := adapter.Parse(adapter.MediaInput{Path: p, Representation: "v", Entry: 0, Manifest: "m.mpd"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if tr.TimeScale != 1000 || tr.Kind != model.TrackVideo || len(tr.Frames) != 4 {
		t.Fatalf("track = %+v", tr)
	}
	if tr.Frames[0].PTS != 0 || tr.Frames[3].PTS != 3_000_000_000 {
		t.Fatalf("pts wrong: %d %d", tr.Frames[0].PTS, tr.Frames[3].PTS)
	}
	if !tr.Frames[0].KeyFrame || tr.Frames[1].KeyFrame || !tr.Frames[3].KeyFrame {
		t.Fatalf("keyframe flags wrong: %+v", tr.Frames)
	}
	if string(tr.Frames[2].Payload) != "p-2" {
		t.Fatalf("payload passthrough broken: %q", tr.Frames[2].Payload)
	}
	if tr.Source.Entry != 0 || tr.Source.File != p {
		t.Fatalf("provenance ref wrong: %+v", tr.Source)
	}
}

func TestMP4TruncatedBox(t *testing.T) {
	frames := []fixture.FrameSpec{{Payload: []byte("k"), Duration: 1000, KeyFrame: true}}
	seg := fixture.BuildSegment(1000, 0, 1, 0, frames)
	// Inflate the final mdat box header past EOF: the container structure is
	// intact but the box is corrupt, which must surface as corrupt_box rather
	// than being silently accepted.
	broken := append([]byte(nil), seg...)
	putU32(broken[len(broken)-len(frames[0].Payload)-8:],
		binaryU32(broken[len(broken)-len(frames[0].Payload)-8:])+10)
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.m4s")
	writeSeg(t, p, broken)
	_, err := adapter.Parse(adapter.MediaInput{Path: p, Representation: "v", Entry: 1, Manifest: "m.mpd"})
	se, ok := model.AsSource(err)
	if !ok {
		t.Fatalf("expected sourced error, got %v", err)
	}
	if se.Kind != model.KindCorruptBox && se.Kind != model.KindTruncatedFrame {
		t.Fatalf("kind = %s", se.Kind)
	}
	if se.Loc.Segment != p || se.Loc.Representation != "v" || se.Loc.Entry != 1 {
		t.Fatalf("loc lost: %+v", se.Loc)
	}
}

func TestMP4TruncatedFramePayload(t *testing.T) {
	// Declared sample size larger than the mdat actually holds.
	frames := []fixture.FrameSpec{
		{Payload: []byte("key"), Duration: 1000, KeyFrame: true},
		{Payload: []byte("x"), Duration: 1000, KeyFrame: false},
	}
	seg := fixture.BuildSegment(1000, 0, 1, 0, frames)
	// Locate mdat header and shrink its declared size by 1 byte so the last
	// sample reaches past the payload.
	mdatPos := len(seg) - 8 - 4 // mdat header(8) + 4 payload bytes
	broken := append([]byte(nil), seg...)
	cur := binaryU32(broken[mdatPos : mdatPos+4])
	putU32(broken[mdatPos:mdatPos+4], cur-1)
	dir := t.TempDir()
	p := filepath.Join(dir, "short.m4s")
	writeSeg(t, p, broken)
	_, err := adapter.Parse(adapter.MediaInput{Path: p, Representation: "v", Entry: 0})
	se, ok := model.AsSource(err)
	if !ok || se.Kind != model.KindTruncatedFrame {
		t.Fatalf("expected truncated-frame, got %v", err)
	}
	if se.Loc.Frame != 1 {
		t.Fatalf("must point at offending frame, got %d", se.Loc.Frame)
	}
}

func TestMP4ZeroTimescale(t *testing.T) {
	frames := []fixture.FrameSpec{{Payload: []byte("k"), Duration: 1, KeyFrame: true}}
	seg := fixture.BuildSegment(0, 1, 1, 0, frames)
	dir := t.TempDir()
	p := filepath.Join(dir, "zero.m4s")
	writeSeg(t, p, seg)
	_, err := adapter.Parse(adapter.MediaInput{Path: p})
	se, ok := model.AsSource(err)
	if !ok || se.Kind != model.KindZeroTimescale {
		t.Fatalf("got %v", err)
	}
}

func TestMP4AudioTrack(t *testing.T) {
	frames := []fixture.FrameSpec{{Payload: []byte("a1"), Duration: 1024, KeyFrame: true}}
	seg := fixture.BuildSegment(48000, 1, 1, 0, frames)
	dir := t.TempDir()
	p := filepath.Join(dir, "a.m4s")
	writeSeg(t, p, seg)
	tr, err := adapter.Parse(adapter.MediaInput{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Kind != model.TrackAudio {
		t.Fatalf("kind = %s", tr.Kind)
	}
}
