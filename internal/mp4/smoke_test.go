package mp4mux

import (
	"os"
	"path/filepath"
	"testing"

	"mediarun/internal/clock"
	"mediarun/internal/frames"
	"mediarun/internal/index"
	"mediarun/internal/syncer"
)

func fixtureReport(t *testing.T, n int) (*syncer.Report, *clock.Timeline) {
	t.Helper()
	tl, err := clock.NewTimeline(90000)
	if err != nil {
		t.Fatal(err)
	}
	vids := make([]*frames.Record, n)
	for i := range vids {
		p := make([]byte, 32)
		copy(p, "IMG1")
		p[4] = byte(i)
		flags := uint16(0)
		if i%5 == 0 {
			flags |= frames.FlagKeyframe
		}
		vids[i] = &frames.Record{
			Offset: int64(i) * 100,
			Index:  i,
			Meta: frames.Metadata{
				Flags: flags, Track: 1,
				PTS: uint64(i * 3000), DTS: uint64(i * 3000),
				Size: uint32(len(p)), Checksum: [32]byte{byte(i)},
			},
			Payload: p,
		}
	}
	rep, err := syncer.Align(vids, nil, tl, syncer.Config{ToleranceTicks: 900, AudioClockRate: 48000, AudioTrack: 2})
	if err != nil {
		t.Fatal(err)
	}
	return rep, tl
}

func TestMuxWritesReadableIndex(t *testing.T) {
	rep, tl := fixtureReport(t, 12)
	path := filepath.Join(t.TempDir(), "out.mp4")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Mux(f, rep, tl, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if res.Samples != 12 {
		t.Fatalf("samples = %d", res.Samples)
	}
	if len(res.Keyframes) != 3 {
		t.Fatalf("keyframes = %d", len(res.Keyframes))
	}
	idx := &index.Index{Segment: 0, Entries: res.Keyframes, VideoTrack: 1, SampleCount: res.Samples}
	vr, err := index.Verify(path, idx)
	if err != nil {
		t.Fatal(err)
	}
	if !vr.Verified() {
		t.Fatalf("index verify misses: %v", vr.Misses)
	}
	if vr.Ratio() != 100 {
		t.Fatalf("ratio = %d", vr.Ratio())
	}
	// A tampered entry must fail.
	idx.Entries[1].Sample++
	vr, err = index.Verify(path, idx)
	if err != nil {
		t.Fatal(err)
	}
	if vr.Verified() {
		t.Fatal("tampered index verified")
	}
}

func TestMuxPreservesOrderAndTimestamps(t *testing.T) {
	rep, tl := fixtureReport(t, 6)
	path := filepath.Join(t.TempDir(), "out.mp4")
	f, _ := os.Create(path)
	res, err := Mux(f, rep, tl, Config{})
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	for i, pts := range res.PTSList {
		if pts != uint64(i*3000) {
			t.Fatalf("sample %d pts = %d", i, pts)
		}
	}
	tbl, err := index.ReadTable(path)
	if err != nil {
		t.Fatal(err)
	}
	offs, err := index.SampleOffsets(tbl)
	if err != nil {
		t.Fatal(err)
	}
	for i, off := range offs {
		if int(off) != int(res.VideoChunkOff)+i*32 {
			t.Fatalf("sample %d offset = %d", i, off)
		}
	}
}
