package mp4index

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	mp4 "github.com/abema/go-mp4"
	"github.com/orcaman/writerseeker"

	"example.com/assetcheck/internal/fragments"
	"example.com/assetcheck/internal/timeline"
)

// box wraps a marshalled MP4 box payload in its 8-byte header.
func box(typ string, parts ...[]byte) []byte {
	n := 8
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 8, n)
	binary.BigEndian.PutUint32(out[0:4], uint32(n))
	copy(out[4:8], typ)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func payload(t *testing.T, src mp4.IImmutableBox) []byte {
	t.Helper()
	ws := &writerseeker.WriterSeeker{}
	if _, err := mp4.Marshal(ws, src, mp4.Context{}); err != nil {
		t.Fatalf("marshal %s: %v", src.GetType().String(), err)
	}
	buf := make([]byte, ws.BytesReader().Len())
	if _, err := ws.BytesReader().Read(buf); err != nil {
		t.Fatal(err)
	}
	return buf
}

// buildSidecar writes a minimal but structurally real MP4 index sidecar: an
// ftyp, a moov with mvhd, and one audio track whose mdhd timescale and stts
// table drive the sample timeline.
func buildSidecar(t *testing.T, timescale uint32, deltas []mp4.SttsEntry) []byte {
	t.Helper()
	ftyp := box("ftyp", []byte("isom\x00\x00\x02\x00isomiso2"))
	mvhd := box("mvhd", payload(t, &mp4.Mvhd{
		Timescale: 1000, DurationV0: 4000, Rate: 0x10000, Volume: 0x100, NextTrackID: 2}))
	hdlr := box("hdlr", payload(t, &mp4.Hdlr{
		HandlerType: [4]byte{'s', 'o', 'u', 'n'}, Name: "SoundHandler"}))
	mdhd := box("mdhd", payload(t, &mp4.Mdhd{
		Timescale: timescale, DurationV0: 1000, Language: [3]byte{'e', 'n', 'g'}}))
	stts := box("stts", payload(t, &mp4.Stts{EntryCount: uint32(len(deltas)), Entries: deltas}))
	stbl := box("stbl", stts)
	minf := box("minf", stbl)
	mdia := box("mdia", hdlr, mdhd, minf)
	tkhd := box("trak", box("tkhd", payload(t, &mp4.Tkhd{
		TrackID: 2, DurationV0: 1000, Volume: 0x100})), mdia)
	moov := box("moov", mvhd, tkhd)
	return append(ftyp, moov...)
}

func writeSidecar(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "index.mp4")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseSidecarMapsSamplesToMediaBase(t *testing.T) {
	// 90 kHz track clock: sample 0 at 0, sample 1 at 9000 ticks = 100 ms.
	data := buildSidecar(t, 90_000, []mp4.SttsEntry{
		{SampleCount: 1, SampleDelta: 9000},
		{SampleCount: 1, SampleDelta: 9000},
	})
	a, err := Parse(writeSidecar(t, data))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if a.Container != "mp4" {
		t.Fatalf("container = %s", a.Container)
	}
	if len(a.Tracks) != 1 || a.Tracks[0].Type.Name() != "audio" {
		t.Fatalf("tracks = %+v", a.Tracks)
	}
	if len(a.Fragments) != 2 {
		t.Fatalf("samples = %d", len(a.Fragments))
	}
	if a.Fragments[0].Start != 0 || a.Fragments[1].Start != 100_000_000 {
		t.Fatalf("starts = %d, %d", a.Fragments[0].Start, a.Fragments[1].Start)
	}
	if a.Fragments[1].Duration != 100_000_000 {
		t.Fatalf("duration = %d", a.Fragments[1].Duration)
	}
	// Element offsets must be real, ascending and inside the file.
	last := int64(-1)
	for _, e := range a.Elements {
		if e.Offset <= last {
			t.Fatalf("element offsets not ascending at %s: %d", e.Name, e.Offset)
		}
		if e.Offset >= a.Size {
			t.Fatalf("element %s offset %d outside %d-byte file", e.Name, e.Offset, a.Size)
		}
		last = e.Offset
	}
	if a.Size != int64(len(data)) {
		t.Fatalf("size = %d, want %d", a.Size, len(data))
	}
}

func TestParseSidecarDetectsMissingMoov(t *testing.T) {
	data := box("ftyp", []byte("isom\x00\x00\x02\x00isomiso2"))
	_, err := Parse(writeSidecar(t, data))
	if err == nil {
		t.Fatal("sidecar without moov accepted")
	}
	if timeline.ExitCodeOf(err) != timeline.ExitBadInput {
		t.Fatalf("exit = %d", timeline.ExitCodeOf(err))
	}
}

func TestParseSidecarZeroTimescaleRejected(t *testing.T) {
	data := buildSidecar(t, 0, []mp4.SttsEntry{{SampleCount: 1, SampleDelta: 1}})
	_, err := Parse(writeSidecar(t, data))
	if err == nil {
		t.Fatal("zero mdhd timescale accepted")
	}
	if timeline.CategoryOf(err) != timeline.CatMissingElement {
		t.Fatalf("category = %s", timeline.CategoryOf(err))
	}
}

func TestParseSidecarTruncatedMidBox(t *testing.T) {
	data := buildSidecar(t, 48000, []mp4.SttsEntry{{SampleCount: 1, SampleDelta: 1024}})
	_, err := Parse(writeSidecar(t, data[:len(data)-6]))
	if err == nil {
		t.Fatal("truncated sidecar accepted")
	}
	if timeline.ExitCodeOf(err) != timeline.ExitBadInput {
		t.Fatalf("exit = %d", timeline.ExitCodeOf(err))
	}
}

func TestSampleTimelinePassesFragmentValidation(t *testing.T) {
	data := buildSidecar(t, 48_000, []mp4.SttsEntry{
		{SampleCount: 3, SampleDelta: 960},
	})
	a, err := Parse(writeSidecar(t, data))
	if err != nil {
		t.Fatal(err)
	}
	if rep := fragments.Validate(a); !rep.OK() {
		t.Fatalf("mp4 sample timeline rejected: %+v", rep.Problems)
	}
}

func TestParseSidecarIsStable(t *testing.T) {
	data := buildSidecar(t, 48_000, []mp4.SttsEntry{{SampleCount: 2, SampleDelta: 1024}})
	path := writeSidecar(t, data)
	a, err := Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if a.SHA256 != b.SHA256 || len(a.Fragments) != len(b.Fragments) {
		t.Fatal("repeated parse differs")
	}
	if !bytes.Equal([]byte(Describe(a)), []byte(Describe(b))) {
		t.Fatal("describe differs")
	}
}
