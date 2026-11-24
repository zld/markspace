package mp4

import (
	"testing"

	mp4 "github.com/abema/go-mp4"
)

// track builds a minimal audio structTrack for hostile-table tests.
func hostileTrack(stsc []mp4.StscEntry, stco []uint32, co64 []uint64, count uint32, constant uint32) structTrack {
	return structTrack{
		id: 1, handler: "soun", mediaTimescale: 48000, sampleType: "ipcm",
		channels: 2, sampleSizeBits: 16,
		stsc: stsc, stco: stco, co64: co64,
		stszConstant: constant, stszCount: count,
	}
}

func buildOne(t *testing.T, tr structTrack, readable, mdatStart int64) (TrackInfo, error) {
	t.Helper()
	ti, _, err := buildTrack(tr, readable, int64(MaxSamplesPerTrack), mdatStart)
	return ti, err
}

// On overlap: a later chunk must not be confirmed just because the adjacent
// chunk was short; the high-water mark of all prior chunks is the bar.
func TestOverlapUsesRunningHighWaterMark(t *testing.T) {
	tr := hostileTrack(
		[]mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: 250}, {FirstChunk: 2, SamplesPerChunk: 25}, {FirstChunk: 3, SamplesPerChunk: 250}},
		[]uint32{0, 500, 700}, nil, 525, 4,
	)
	ti, err := buildOne(t, tr, 2000, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Chunk 3 starts at 700, after chunk 2 ended at 600, but chunk 1 spans
	// [0,1000).  Samples of chunk 3 must therefore be unconfirmed.
	var chunk3Unconfirmed int
	for _, s := range ti.Samples {
		if s.Offset >= 700 && s.Offset < 1700 && !s.Confirmed {
			chunk3Unconfirmed++
		}
	}
	if chunk3Unconfirmed == 0 {
		t.Fatal("chunk 3 overlapped chunk 1 but was confirmed")
	}
}

func TestNoMdatRebuildRefused(t *testing.T) {
	tr := hostileTrack([]mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: 250}}, nil, nil, 250, 4)
	// mdatStart -1 means no mdat; the rebuild must not index over ftyp/moov.
	if _, err := buildOne(t, tr, 100000, -1); err == nil {
		t.Fatal("expected refusal to rebuild without mdat")
	}
}

func TestCo64OverflowNotConfirmed(t *testing.T) {
	huge := uint64(0x7FFFFFFFFFFFFFFF)
	tr := hostileTrack([]mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: 2}}, nil, []uint64{huge}, 2, 4)
	ti, err := buildOne(t, tr, 1000, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range ti.Samples {
		if s.Confirmed {
			t.Fatalf("overflowing co64 offset %d was confirmed", s.Offset)
		}
	}
}

func TestZeroSamplesPerChunkKeepsOffsetAlignment(t *testing.T) {
	// stsc with a zero middle run must not shift later chunk offsets.
	tr := hostileTrack(
		[]mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: 10}, {FirstChunk: 2, SamplesPerChunk: 0}, {FirstChunk: 3, SamplesPerChunk: 10}},
		[]uint32{100, 200, 300}, nil, 20, 4,
	)
	ti, err := buildOne(t, tr, 10000, 0)
	if err != nil {
		t.Fatal(err)
	}
	// The last 10 samples belong to chunk 3 at offset 300, not chunk 2 at 200.
	foundAt300 := false
	for _, s := range ti.Samples {
		if s.Offset == 300 && s.Confirmed {
			foundAt300 = true
		}
		if s.Offset == 200 {
			t.Fatalf("sample placed at the zero-run chunk offset 200")
		}
	}
	if !foundAt300 {
		t.Fatal("chunk 3 samples were not placed at offset 300")
	}
}

func TestNoOffsetTableLeadingZeroRunDoesNotDropTrack(t *testing.T) {
	tr := hostileTrack([]mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: 0}}, nil, nil, 250, 4)
	ti, err := buildOne(t, tr, 10000, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(ti.Samples) == 0 {
		t.Fatal("a leading zero run dropped the whole track")
	}
}

func TestStscRunAtPreservesFirstChunk(t *testing.T) {
	// A run beginning at chunk 5 must not be applied to chunks 1-4 as if it
	// began at chunk 1.
	got := stscRunAt([]mp4.StscEntry{{FirstChunk: 5, SamplesPerChunk: 10}}, 1)
	if got.FirstChunk != 5 {
		t.Fatalf("stscRunAt returned FirstChunk %d for chunk 1", got.FirstChunk)
	}
}

func TestIndexShortRecordsFault(t *testing.T) {
	// stsc implies 3 chunks but stco provides only 1; the rebuild must be
	// reported, not silent.
	tr := hostileTrack([]mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: 4}}, []uint32{100}, nil, 12, 4)
	_, fs, err := buildTrack(tr, 10000, int64(MaxSamplesPerTrack), 8)
	if err != nil {
		t.Fatal(err)
	}
	recorded := false
	for _, f := range fs {
		if f.Code == "index-short" || f.Code == "index-missing" || f.Code == "samples-unplaced" || f.Code == "stsc-missing" {
			recorded = true
		}
	}
	if !recorded {
		t.Fatalf("index inconsistency not reported: %v", fs)
	}
}
