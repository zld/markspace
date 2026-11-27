package mux_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/abema/go-mp4"
	"github.com/zld/continuity/internal/clock"
	"github.com/zld/continuity/internal/demux"
	"github.com/zld/continuity/internal/mux"
)

func videoTrack(n int, every int) mux.TrackSpec {
	specs := make([]mux.SampleSpec, n)
	for i := range specs {
		specs[i] = mux.SampleSpec{Delta: 1, Sync: every > 0 && i%every == 0}
	}
	return mux.TrackSpec{Timescale: 25, Video: true, Samples: specs}
}

func buildFixture(t *testing.T, tracks []mux.TrackSpec, fault mux.Faults) []byte {
	t.Helper()
	var buf bytes.Buffer
	_, err := mux.WriteFile(&buf, tracks, fault)
	if fault != mux.FaultNone && err != nil {
		// Truncation is expected to surface as an incomplete write; keep the
		// partial image for negative parsing tests.
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("unexpected fixture error: %v", err)
		}
	} else if err != nil {
		t.Fatalf("fixture build: %v", err)
	}
	return buf.Bytes()
}

func writeTemp(t *testing.T, data []byte) string {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/fixture.mp4"
	if err := writeAll(path, data); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMuxProducesParseableSingleTrack(t *testing.T) {
	img := buildFixture(t, []mux.TrackSpec{videoTrack(12, 3)}, mux.FaultNone)
	path := writeTemp(t, img)

	f, err := demux.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	if len(f.Tracks) != 1 {
		t.Fatalf("tracks = %d", len(f.Tracks))
	}
	tr := f.Tracks[0]
	if tr.Timescale != 25 || !tr.Video || len(tr.Samples) != 12 {
		t.Fatalf("track header wrong: %+v", tr)
	}
	if len(tr.SyncRows) != 4 { // samples 1,4,7,10
		t.Fatalf("sync rows = %d", len(tr.SyncRows))
	}
	for i, s := range tr.Samples {
		if s.Size != 64 || !s.Present {
			t.Fatalf("sample %d metadata wrong: %+v", i, s)
		}
		if s.DTS.Cmp(clock.MustMake(int64(i), 25)) != 0 {
			t.Fatalf("sample %d dts %s", i, s.DTS)
		}
		if s.Sync != (i%3 == 0) {
			t.Fatalf("sample %d sync flag %v", i, s.Sync)
		}
	}
}

func TestMuxOffsetsPointAtPayload(t *testing.T) {
	img := buildFixture(t, []mux.TrackSpec{videoTrack(6, 2)}, mux.FaultNone)
	path := writeTemp(t, img)
	f, err := demux.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// Re-read each sample independently and check the deterministic bytes.
	for i := range f.Tracks[0].Samples {
		raw, err := f.ReadSample(0, uint32(i))
		if err != nil {
			t.Fatalf("read sample %d: %v", i, err)
		}
		if len(raw) != 64 {
			t.Fatalf("sample %d size %d", i, len(raw))
		}
		if int(raw[0]) != (0*31+i*17+0*7)&0xff {
			t.Fatalf("sample %d payload mismatch, first byte %d", i, raw[0])
		}
		want := byte((0*31 + i*17 + 0*7) & 0xff)
		if raw[0] != want {
			t.Fatalf("sample %d first byte = %d, want %d", i, raw[0], want)
		}
	}
}

func TestMuxMultiTrackInterleaving(t *testing.T) {
	v := videoTrack(8, 4)
	a := mux.TrackSpec{Timescale: 48000, Video: false, Samples: audioSpecs(8)}
	img := buildFixture(t, []mux.TrackSpec{v, a}, mux.FaultNone)
	path := writeTemp(t, img)
	f, err := demux.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if len(f.Tracks) != 2 || f.Tracks[1].Video || f.Tracks[1].Timescale != 48000 {
		t.Fatalf("audio track wrong: %+v", f.Tracks)
	}
	// Both tracks' offsets must remain inside the file and never overlap.
	seen := map[uint64]bool{}
	for _, tr := range f.Tracks {
		for _, s := range tr.Samples {
			if s.FileOffset+uint64(s.Size) > uint64(len(img)) {
				t.Fatalf("offset %d outside file %d", s.FileOffset, len(img))
			}
			key := s.FileOffset
			if seen[key] {
				t.Fatalf("duplicate offset %d", key)
			}
			seen[key] = true
		}
	}
}

func TestMuxStructuralFaultsRejected(t *testing.T) {
	cases := map[string]mux.Faults{
		"truncate":     mux.FaultTruncateStbl,
		"bad-stts":     mux.FaultBadSttsCount,
		"zero-timescale": mux.FaultZeroTimescale,
	}
	for name, fault := range cases {
		t.Run(name, func(t *testing.T) {
			img := buildFixture(t, []mux.TrackSpec{videoTrack(10, 2)}, fault)
			path := writeTemp(t, img)
			_, err := demux.Open(path)
			if err == nil {
				t.Fatal("expected corrupt input to be rejected")
			}
			if !errorIs(err, clock.ErrCorrupt) {
				t.Fatalf("%s: want ErrCorrupt, got %v", name, err)
			}
		})
	}
}

func TestMuxBoxesAreWellFormed(t *testing.T) {
	img := buildFixture(t, []mux.TrackSpec{videoTrack(4, 1)}, mux.FaultNone)
	boxes, err := mp4.ExtractBoxes(bytes.NewReader(img), nil,
		[]mp4.BoxPath{{mp4.StrToBoxType("ftyp")}, {mp4.StrToBoxType("moov")}, {mp4.StrToBoxType("mdat")}})
	if err != nil {
		t.Fatalf("top-level box scan: %v", err)
	}
	if len(boxes) != 3 {
		t.Fatalf("top-level boxes = %d, want 3", len(boxes))
	}
}
