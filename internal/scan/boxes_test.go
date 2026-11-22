package scan

import (
	"bytes"
	"encoding/binary"
	"testing"

	"mediacore/internal/clock"
)

// bytesReaderAt wraps bytes.Reader to also satisfy io.ReaderAt (it does).
func box(size uint32, typ string, payload []byte) []byte {
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr[0:4], size)
	copy(hdr[4:8], typ)
	return append(hdr, payload...)
}

func TestWalkBoxesAcceptsValidHierarchy(t *testing.T) {
	// ftyp(16) + moov { mvhd(8) } => moov size 16.
	ftyp := box(16, "ftyp", make([]byte, 8))
	mvhd := box(8, "mvhd", nil)
	moov := box(16, "moov", mvhd)
	data := append(ftyp, moov...)
	var seen int
	err := WalkBoxes(bytes.NewReader(data), uint64(len(data)), func(h BoxHeader, depth int) error {
		seen++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen != 3 {
		t.Fatalf("visited %d boxes, want 3", seen)
	}
}

func TestWalkBoxesRejectsIllegalLength(t *testing.T) {
	// Declared size beyond EOF.
	data := box(4096, "ftyp", make([]byte, 8))
	err := WalkBoxes(bytes.NewReader(data), uint64(len(data)), func(BoxHeader, int) error { return nil })
	if clock.Classify(err) != clock.KindRecoverable {
		t.Fatalf("want recoverable, got %v", err)
	}
}

func TestWalkBoxesRejectsSizeBelowHeader(t *testing.T) {
	data := box(4, "moov", nil)
	err := WalkBoxes(bytes.NewReader(data), uint64(len(data)), func(BoxHeader, int) error { return nil })
	if clock.Classify(err) != clock.KindRecoverable {
		t.Fatalf("want recoverable, got %v", err)
	}
}

func TestWalkBoxesRejectsUnalignedTrailingBytes(t *testing.T) {
	good := box(16, "ftyp", make([]byte, 8))
	data := append(good, 0xaa, 0xbb) // 2 trailing bytes, no box
	err := WalkBoxes(bytes.NewReader(data), uint64(len(data)), func(BoxHeader, int) error { return nil })
	if clock.Classify(err) != clock.KindRecoverable {
		t.Fatalf("want recoverable, got %v", err)
	}
}

func TestSeekDecodableFallback(t *testing.T) {
	dts := func(idx uint32) clock.Nanos {
		return clock.Nanos(int64(idx) * 33_333_333)
	}
	// Sync at samples 1, 31, 61 (1-based). Target 45 (0-based) -> anchor 30.
	sync := []uint32{1, 31, 61}
	res, err := SeekDecodable(1, 45, 100, sync, dts)
	if err != nil {
		t.Fatal(err)
	}
	if res.AnchorIndex != 30 || res.FallbackSamples != 15 {
		t.Fatalf("anchor=%d fallback=%d", res.AnchorIndex, res.FallbackSamples)
	}
	if res.FallbackNanos <= 0 {
		t.Fatal("fallback duration not quantified")
	}
}

func TestSeekDecodableMissingAnchor(t *testing.T) {
	// First sync only at sample 10; target sample 2 has no anchor.
	_, err := SeekDecodable(1, 2, 100, []uint32{10}, func(uint32) clock.Nanos { return 0 })
	if clock.Classify(err) != clock.KindRecoverable {
		t.Fatalf("want recoverable, got %v", err)
	}
}

func TestSyncNumbersSorted(t *testing.T) {
	tr := &TrackScan{tables: &sampleTables{sync: map[uint32]struct{}{3: {}, 1: {}, 2: {}}}}
	got := tr.SyncNumbers()
	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("unsorted: %v", got)
	}
}
