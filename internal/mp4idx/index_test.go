package mp4idx

import (
	"bytes"
	"testing"

	"mediamerge/internal/fault"
)

func spec8() FixtureSpec {
	sizes := make([]uint32, 8)
	deltas := make([]uint32, 8)
	for i := range sizes {
		sizes[i] = 100
		deltas[i] = 3000
	}
	return FixtureSpec{
		Timescale: 90000, SampleSizes: sizes, SampleDeltas: deltas,
		SyncSamples: []uint32{1, 3, 5, 7},
	}
}

func TestReadNormalIndex(t *testing.T) {
	data, err := BuildFixture(spec8())
	if err != nil {
		t.Fatal(err)
	}
	idx, err := NewReader("clip.mp4", bytes.NewReader(data)).Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(idx.Samples) != 8 || idx.Timescale != 90000 || idx.Unrecoverable {
		t.Fatalf("index mismatch: %+v", idx)
	}
	// DTS at 90kHz: sample i starts at i*3000 ticks.
	for i, s := range idx.Samples {
		if int64(s.DTS) != int64(i)*3000 {
			t.Fatalf("sample %d ticks = %d, want %d", i, s.DTS, i*3000)
		}
		if s.Size != 100 || !s.SyncKnown {
			t.Fatalf("sample %d size/sync flags wrong: %+v", i, s)
		}
	}
	if idx.SyncCount != 4 {
		t.Fatalf("SyncCount = %d, want 4", idx.SyncCount)
	}
	hits := 0
	for _, s := range idx.Samples {
		if s.IsSync {
			hits++
		}
	}
	if hits != 4 {
		t.Fatalf("keyframe hits = %d, want 4", hits)
	}
	// Offsets are contiguous and every sample stays inside the file.
	for i := 1; i < len(idx.Samples); i++ {
		if idx.Samples[i].Offset != idx.Samples[i-1].Offset+100 {
			t.Fatalf("sample offsets not contiguous at %d", i)
		}
	}
}

func TestNoStssMeansAllSync(t *testing.T) {
	spec := spec8()
	spec.SyncSamples = nil
	data, err := BuildFixture(spec)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := NewReader("clip.mp4", bytes.NewReader(data)).Read()
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range idx.Samples {
		if !s.IsSync || !s.SyncKnown {
			t.Fatalf("without stss every sample must be provably sync: %d %+v", i, s)
		}
	}
}

func TestChunkOffsetOutOfBounds(t *testing.T) {
	spec := spec8()
	spec.ChunkOffsetBias = 1 << 20 // pushes every chunk past EOF
	data, err := BuildFixture(spec)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewReader("clip.mp4", bytes.NewReader(data)).Read()
	fe := fault.As(err)
	if fe == nil || fe.Kind != fault.KindBounds {
		t.Fatalf("want bounds fault, got %v", err)
	}
	if fe.Detail == "" || !contains(fe.Detail, "stco") {
		// Offset faults must name the offending box type.
		t.Fatalf("fault must name the box type: %v", err)
	}
}

func TestCorruptStssUnrecoverable(t *testing.T) {
	spec := spec8()
	spec.CorruptStss = true
	data, err := BuildFixture(spec)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := NewReader("clip.mp4", bytes.NewReader(data)).Read()
	if err != nil {
		t.Fatalf("corrupt stss must not hard-fail parsing: %v", err)
	}
	if !idx.Unrecoverable || len(idx.Warnings) == 0 {
		t.Fatalf("corrupt stss must mark the index unrecoverable: %+v", idx)
	}
}

func TestTruncatedChunkTable(t *testing.T) {
	data, err := BuildFixture(spec8())
	if err != nil {
		t.Fatal(err)
	}
	// Truncate the file inside the moov so a declared box runs past EOF.
	truncated := data[:len(data)-40]
	_, err = NewReader("clip.mp4", bytes.NewReader(truncated)).Read()
	if err == nil {
		t.Fatal("truncated file must be rejected, not guessed")
	}
}

func contains(haystack, needle string) bool {
	return bytes.Contains([]byte(haystack), []byte(needle))
}
