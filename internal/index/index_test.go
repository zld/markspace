package index

import (
	"os"
	"path/filepath"
	"testing"

	"mediakernel/internal/clock"
	"mediakernel/internal/fixture"
	"mediakernel/internal/wav"
)

func openFixtureWAV(t *testing.T, o fixture.WAVOptions) (*wav.Reader, string) {
	t.Helper()
	buf, err := fixture.BuildWAV(o)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "in.wav")
	if err := os.WriteFile(p, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := wav.Open(dir, "in.wav")
	if err != nil {
		t.Fatal(err)
	}
	return r, dir
}

func TestBuildSparseIndexAndDigest(t *testing.T) {
	r, _ := openFixtureWAV(t, fixture.WAVOptions{
		Channels: 2, SampleRate: 48000, Frames: 1000, BitsPerSample: 16,
	})
	defer r.Close()
	tl := clock.NewTimeline(48000)
	idx, err := Build(r, tl, 256)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Entries) != 4 || idx.Entries[0].FrameStart != 0 ||
		idx.Entries[3].FrameStart != 768 || idx.Entries[3].FrameCount != 232 {
		t.Fatalf("entries = %+v", idx.Entries)
	}
	// Anchor time: frame 256 -> 256/48000 s = 5,333,333 ns.
	if idx.Entries[1].MediaNanos != int64(256)*clock.NanosPerSecond/48000 {
		t.Fatalf("anchor ns = %d", idx.Entries[1].MediaNanos)
	}
	d1, d2 := idx.Digest(), idx.Digest()
	if d1 != d2 || len(d1) != 64 {
		t.Fatal("digest unstable or wrong length")
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	r, _ := openFixtureWAV(t, fixture.WAVOptions{
		Channels: 1, SampleRate: 8000, Frames: 500, BitsPerSample: 16,
	})
	defer r.Close()
	idx, err := Build(r, clock.NewTimeline(8000), 100)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := Unmarshal(idx.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if restored.Digest() != idx.Digest() {
		t.Fatal("round-trip digest mismatch")
	}
	if _, err := Unmarshal(append([]byte("BADMAGIC"), make([]byte, 40)...)); err == nil {
		t.Fatal("bad magic must fail")
	}
	if _, err := Unmarshal([]byte("MKIDX001")); err == nil {
		t.Fatal("truncated payload must fail")
	}
}

func TestLocateAndReadRangeBoundaries(t *testing.T) {
	r, _ := openFixtureWAV(t, fixture.WAVOptions{
		Channels: 2, SampleRate: 48000, Frames: 960, BitsPerSample: 16,
	})
	defer r.Close()
	tl := clock.NewTimeline(48000)
	idx, err := Build(r, tl, 240) // 4 blocks
	if err != nil {
		t.Fatal(err)
	}
	// Frame 300 sits inside the block starting at 240.
	block, err := idx.LocateBlock(300)
	if err != nil || block.FrameStart != 240 {
		t.Fatalf("locate = %+v err=%v", block, err)
	}
	if _, err := idx.LocateBlock(960); err == nil {
		t.Fatal("frame at total boundary must be rejected")
	}
	// Half-open 10 ms interval [0,10ms): exactly 480 frames, 1920 bytes.
	res, err := idx.ReadRange(r, tl, 0, 10_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if res.Range.Start != 0 || res.Range.End != 480 || len(res.Bytes) != 1920 {
		t.Fatalf("range = %+v bytes=%d", res.Range, len(res.Bytes))
	}
	if res.StartBlock.FrameStart != 0 {
		t.Fatal("start block must cover frame 0")
	}
	// Adjacent ranges are gap-free.
	res2, err := idx.ReadRange(r, tl, 10_000_000, 20_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Range.Start != 480 || res2.Range.End != 960 {
		t.Fatalf("second range = %+v", res2.Range)
	}
}

func TestReadRangeTruncationLocatesOffset(t *testing.T) {
	good, _ := fixture.BuildWAV(fixture.WAVOptions{
		Channels: 2, SampleRate: 48000, Frames: 960, BitsPerSample: 16,
	})
	bad, err := fixture.TruncateTail(good, 2) // mid-frame cut
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "trunc.wav")
	if err := os.WriteFile(p, bad, 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := wav.Open(dir, "trunc.wav")
	if err != nil {
		// Eager RIFF-size detection is acceptable and must carry the offset.
		ce, ok := clock.AsError(err)
		if !ok || ce.Category != clock.CatTruncated || ce.ByteOffset != int64(len(bad)) {
			t.Fatalf("eager truncation = %v", err)
		}
		return
	}
	defer r.Close()
	tl := clock.NewTimeline(48000)
	idx, err := Build(r, tl, 240)
	if err != nil {
		ce, ok := clock.AsError(err)
		if !ok || ce.Category != clock.CatTruncated {
			t.Fatalf("build truncation = %v", err)
		}
		return
	}
	_, err = idx.ReadRange(r, tl, 0, 20_000_000)
	ce, ok := clock.AsError(err)
	if !ok || ce.Category != clock.CatTruncated || !ce.HasOffset {
		t.Fatalf("range read must report located truncation, got %v", err)
	}
}

func TestReadRangeGeometryMismatchRefused(t *testing.T) {
	r, dir := openFixtureWAV(t, fixture.WAVOptions{
		Channels: 1, SampleRate: 8000, Frames: 200, BitsPerSample: 16,
	})
	defer r.Close()
	idx, err := Build(r, clock.NewTimeline(8000), 50)
	if err != nil {
		t.Fatal(err)
	}
	// Open a different WAV (2 channels) against the 1-channel index.
	buf, _ := fixture.BuildWAV(fixture.WAVOptions{
		Channels: 2, SampleRate: 8000, Frames: 200, BitsPerSample: 16,
	})
	p2 := filepath.Join(dir, "other.wav")
	if err := os.WriteFile(p2, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	r2, err := wav.Open(dir, "other.wav")
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	if _, err := idx.ReadRange(r2, clock.NewTimeline(8000), 0, 10_000_000); err == nil {
		t.Fatal("geometry mismatch must refuse the interval read")
	}
}
