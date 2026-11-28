package container_test

import (
	"path/filepath"
	"testing"

	"rtptrace/internal/container"
	"rtptrace/internal/fault"
	"rtptrace/internal/fixture"
)

func testSpecs() []fixture.TrackSpec {
	return []fixture.TrackSpec{
		{TrackID: 1, Timescale: 48000, SampleCount: 12, SampleSize: 20,
			SamplesPerChunk: 4, Delta: 480, SSRC: 111, PayloadType: 96},
		{TrackID: 2, Timescale: 90000, SampleCount: 6, SampleSize: 32,
			SamplesPerChunk: 2, Delta: 3000, SSRC: 222, PayloadType: 97},
	}
}

func TestScanValidContainer(t *testing.T) {
	dir := t.TempDir()
	f, err := fixture.BuildMP4(dir, 17, testSpecs())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	c, err := container.ScanFile(f.Path)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(c.Tracks) != 2 {
		t.Fatalf("tracks=%d want 2", len(c.Tracks))
	}
	tr0 := c.Tracks[0]
	if tr0.Timescale != 48000 || tr0.SampleCount != 12 {
		t.Fatalf("track0 metadata wrong: %+v", tr0)
	}
	if len(c.Mdat) != 1 {
		t.Fatalf("mdat ranges=%d want 1", len(c.Mdat))
	}
	// DTS must follow stts deltas exactly.
	for i, s := range tr0.Samples {
		if s.DTS != uint64(i)*480 {
			t.Fatalf("sample %d dts=%d want %d", i, s.DTS, i*480)
		}
		if s.Size != 20 {
			t.Fatalf("sample %d size=%d want 20", i, s.Size)
		}
		if wantSync := i == 0; s.Sync != wantSync {
			t.Fatalf("sample %d sync=%v want %v", i, s.Sync, wantSync)
		}
	}
	// Chunk boundaries: 12 samples / 4 per chunk => contiguous offsets.
	if s := tr0.Samples[4]; s.Offset != tr0.Samples[0].Offset+4*20 {
		t.Fatalf("chunk offset layout wrong: %d vs %d", s.Offset, tr0.Samples[0].Offset)
	}
}

func TestScanTruncatedBox(t *testing.T) {
	dir := t.TempDir()
	f, err := fixture.BuildMP4(dir, 17, testSpecs())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// Cut inside the final stco entry: the table's declared entry runs past
	// EOF, so the scanner must report a bad_offset within 8 bytes of the cut.
	cut := f.TruncatePointInTable()
	dst := filepath.Join(dir, "trunc.mp4")
	if err := fixture.TruncateCopy(f.Path, dst, cut); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	_, err = container.ScanFile(dst)
	if err == nil {
		t.Fatal("expected corruption error")
	}
	fe, ok := fault.AsError(err)
	if !ok || fe.Kind != fault.KindCorrupt {
		t.Fatalf("not a corrupt fault: %v", err)
	}
	if fe.Offset < 0 {
		t.Fatalf("error must carry bad offset: %v", err)
	}
	diff := fe.Offset - cut
	if diff < 0 {
		diff = -diff
	}
	if diff > 8 {
		t.Fatalf("bad_offset=%d differs from cut %d by %d (>8)", fe.Offset, cut, diff)
	}
}

func TestScanOutOfBoundsSampleTable(t *testing.T) {
	dir := t.TempDir()
	f, err := fixture.BuildMP4(dir, 17, testSpecs())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	off, err := fixture.MutateChunkOffset(f, 0, 1, 0xFFFFFFF0)
	if err != nil {
		t.Fatalf("mutate: %v", err)
	}
	_, err = container.ScanFile(f.Path)
	if err == nil {
		t.Fatal("expected out-of-bounds error")
	}
	fe, ok := fault.AsError(err)
	if !ok || fe.Kind != fault.KindCorrupt || fe.TrackIndex != 0 {
		t.Fatalf("want track-scoped corrupt fault, got %v", err)
	}
	// Reported position must be the offending stco entry itself.
	diff := fe.Offset - off
	if diff < 0 {
		diff = -diff
	}
	if diff > 8 {
		t.Fatalf("bad_offset=%d vs entry offset %d diff %d", fe.Offset, off, diff)
	}
}

func TestScanDeterministicDigest(t *testing.T) {
	dir := t.TempDir()
	f1, err := fixture.BuildMP4(filepath.Join(dir, "a"), 17, testSpecs())
	if err != nil {
		t.Fatalf("build a: %v", err)
	}
	f2, err := fixture.BuildMP4(filepath.Join(dir, "b"), 17, testSpecs())
	if err != nil {
		t.Fatalf("build b: %v", err)
	}
	c1, err := container.ScanFile(f1.Path)
	if err != nil {
		t.Fatalf("scan a: %v", err)
	}
	c2, err := container.ScanFile(f2.Path)
	if err != nil {
		t.Fatalf("scan b: %v", err)
	}
	if c1.SHA256 != c2.SHA256 {
		t.Fatal("same seed must yield identical input digest")
	}
}
