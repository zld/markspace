package ebml

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// --- minimal EBML writer used only by tests ---

// encodeVint emits a plain data value with a width-bit marker ORed in. Used
// for size VINTs and block track numbers (data values without markers).
func encodeVint(v uint64, width int) []byte {
	b := make([]byte, width)
	x := v
	for i := width - 1; i >= 0; i-- {
		b[i] = byte(x)
		x >>= 8
	}
	b[0] |= 0x80 >> uint(width-1)
	return b
}

// vintID encodes an element ID constant. Matroska ID constants already carry
// their VINT marker (e.g. 0x4282, 0x1A45DFA3); width is derived from the
// marker position and the bytes are emitted verbatim.
func vintID(v uint64) []byte {
	// Canonical Matroska IDs are complete VINT values including their marker.
	// A width-N VINT stores 7N data bits with the marker at bit 7N; scan
	// widest-first so a low data bit is never mistaken for a narrow marker.
	for width := 4; width >= 1; width-- {
		markerBit := uint(7 * width)
		if v&(uint64(1)<<markerBit) != 0 {
			b := make([]byte, width)
			x := v
			for i := width - 1; i >= 0; i-- {
				b[i] = byte(x)
				x >>= 8
			}
			return b
		}
	}
	panic("id vint without marker bits")
}

// vintSize encodes a size VINT, reserving the all-ones form for unknown size.
func vintSize(v uint64) []byte {
	for width := 1; width <= 8; width++ {
		max := uint64(1) << uint(7*width)
		if v < max-1 {
			return encodeVint(v, width)
		}
	}
	panic("size vint too large")
}

// vint keeps the old name for call sites that encode track numbers / small IDs.
func vint(v uint64) []byte {
	for width := 1; width <= 8; width++ {
		if v < (uint64(1)<<uint(7*width))-1 {
			return encodeVint(v, width)
		}
	}
	panic("data vint too large")
}

func elem(id uint32, body []byte) []byte {
	return append(vintID(uint64(id)), append(vintSize(uint64(len(body))), body...)...)
}

func unknownElem(id uint32, body []byte) []byte {
	idb := vintID(uint64(id))
	w := len(idb)
	size := make([]byte, w)
	for i := range size {
		size[i] = 0xFF
	}
	// All data bits set plus the length marker => unknown-length VINT.
	size[0] = 0xFF >> uint(w-1)
	return append(idb, append(size, body...)...)
}

func beUint(v uint64, n int) []byte {
	b := make([]byte, n)
	for i := n - 1; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
	return b
}

func blockBody(track uint64, rel int16, keyframe bool, frame []byte) []byte {
	flags := byte(0)
	if keyframe {
		flags = 0x80
	}
	head := append(vint(track), beUint(uint64(uint16(rel)), 2)...)
	head = append(head, flags)
	return append(head, frame...)
}

// buildWebM assembles a two-track, two-cluster WebM-like byte stream.
// unknownLen makes the Segment use an unknown-length VINT.
func buildWebM(t *testing.T, unknownLen bool) []byte {
	t.Helper()
	ebmlHdr := elem(IDEBML,
		elem(IDEBMLDocType, []byte("webm")))
	info := elem(IDInfo,
		append(elem(IDTimecodeScale, beUint(1_000_000, 4)),
			append(elem(IDMuxingApp, []byte("mt-test")), elem(IDWritingApp, []byte("mt-test"))...)...))
	audio := elem(IDAudio,
		append(elem(IDSamplingFreq, beFloat64(48000)), elem(IDChannels, beUint(2, 1))...))
	t1 := elem(IDTrackEntry,
		append(append(elem(IDTrackNumber, beUint(1, 1)),
			elem(IDTrackType, beUint(2, 1))...),
			append(elem(IDCodecID, []byte("A_PCM/INT/LIT")), audio...)...))
	t2 := elem(IDTrackEntry,
		append(append(elem(IDTrackNumber, beUint(2, 1)),
			elem(IDTrackType, beUint(2, 1))...),
			append(elem(IDCodecID, []byte("A_PCM/INT/LIT")), audio...)...))
	tracks := elem(IDTracks, append(t1, t2...))
	c1 := elem(IDCluster,
		append(elem(IDTimecode, beUint(0, 4)),
			append(elem(IDSimpleBlock, blockBody(1, 0, true, []byte("AAAA"))),
				elem(IDSimpleBlock, blockBody(2, 0, true, []byte("BBBB")))...)...))
	c2 := elem(IDCluster,
		append(elem(IDTimecode, beUint(1000, 4)),
			append(elem(IDSimpleBlock, blockBody(1, 0, true, []byte("CCCC"))),
				elem(IDSimpleBlock, blockBody(2, 0, true, []byte("DDDD")))...)...))
	segmentBody := append(append(info, tracks...), append(c1, c2...)...)
	var seg []byte
	if unknownLen {
		seg = unknownElem(IDSegment, segmentBody)
	} else {
		seg = elem(IDSegment, segmentBody)
	}
	return append(ebmlHdr, seg...)
}

func beFloat64(v float64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, float64bits(v))
	return b
}

func TestScanHappyPath(t *testing.T) {
	data := buildWebM(t, false)
	var got []Block
	doc, err := Scan(bytesReaderAt(data), int64(len(data)), DefaultLimits, func(b Block) error {
		got = append(got, b)
		return nil
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if doc.DocType != "webm" || doc.TimecodeScale != 1_000_000 {
		t.Fatalf("doc metadata wrong: %+v", doc)
	}
	if len(doc.Tracks) != 2 || !doc.Tracks[0].IsAudio || doc.Tracks[0].Rate != 48000 {
		t.Fatalf("tracks wrong: %+v", doc.Tracks)
	}
	if len(got) != 4 || doc.BlockCount != 4 {
		t.Fatalf("want 4 blocks, got %d (doc=%d)", len(got), doc.BlockCount)
	}
	if got[0].TrackNo != 1 || got[0].TimeNs != 0 || got[0].DataSize != 4 {
		t.Fatalf("block0 wrong: %+v", got[0])
	}
	if got[3].TimeNs != 1_000_000_000 || got[3].TrackNo != 2 {
		t.Fatalf("block3 time wrong: %+v", got[3])
	}
	// Frame offset must point at the actual payload bytes.
	if string(data[got[0].DataOffset:got[0].DataOffset+4]) != "AAAA" {
		t.Fatal("data offset does not point at frame bytes")
	}
}

func TestScanUnknownLengthSegment(t *testing.T) {
	data := buildWebM(t, true)
	doc, err := Scan(bytesReaderAt(data), int64(len(data)), DefaultLimits, func(Block) error { return nil })
	if err != nil {
		t.Fatalf("unknown-length segment: %v", err)
	}
	if doc.BlockCount != 4 {
		t.Fatalf("want 4 blocks, got %d", doc.BlockCount)
	}
}

func TestScanTruncatedVINT(t *testing.T) {
	data := buildWebM(t, false)
	// Cut inside the last block's VINT/body region: drop trailing bytes so the
	// final element header cannot be fully read.
	cut := int64(len(data)) - 2
	_, err := Scan(bytesReaderAt(data[:cut]), cut, DefaultLimits, func(Block) error { return nil })
	pe, ok := AsError(err)
	if !ok {
		t.Fatalf("want *Error, got %v", err)
	}
	if pe.Code != ErrTruncatedElem && pe.Code != ErrTruncatedVINT {
		t.Fatalf("want truncation code, got %s", pe.Code)
	}
	if pe.Offset < 0 || pe.Offset >= cut {
		t.Fatalf("bad offset %d", pe.Offset)
	}
}

func TestScanIllegalVINT(t *testing.T) {
	data := buildWebM(t, false)
	// Corrupt the Segment element ID into an illegal >8-byte VINT (0x00 lead).
	segOff := int64(len(elem(IDEBML, elem(IDEBMLDocType, []byte("webm")))))
	data[segOff] = 0x00
	_, err := Scan(bytesReaderAt(data), int64(len(data)), DefaultLimits, func(Block) error { return nil })
	pe, ok := AsError(err)
	if !ok || pe.Code != ErrInvalidVINT {
		t.Fatalf("want ErrInvalidVINT at %d, got %v", segOff, err)
	}
	if pe.Offset != segOff {
		t.Fatalf("offset: want %d got %d", segOff, pe.Offset)
	}
}

func TestScanLimits(t *testing.T) {
	data := buildWebM(t, false)
	lim := DefaultLimits
	lim.MaxFileBytes = 10
	_, err := Scan(bytesReaderAt(data), int64(len(data)), lim, func(Block) error { return nil })
	pe, ok := AsError(err)
	if !ok || pe.Code != ErrLimitExceeded {
		t.Fatalf("want limit error, got %v", err)
	}
}

func TestIndexRoundTripAndCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index")
	sha := make([]byte, 32)
	rand.Read(sha)
	b, err := NewIndexBuilder(path, sha)
	if err != nil {
		t.Fatal(err)
	}
	entries := []IndexEntry{
		{TrackNo: 1, TimeNs: 0, ElemOffset: 10, DataOffset: 20, DataSize: 4},
		{TrackNo: 2, TimeNs: 1_000_000_000, ElemOffset: 40, DataOffset: 52, DataSize: 8},
	}
	for _, e := range entries {
		if err := b.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temp file survived commit")
	}
	x, err := OpenIndex(path, sha)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if x.Count() != 2 {
		t.Fatalf("count=%d", x.Count())
	}
	got, err := x.Entry(1)
	if err != nil || got.TimeNs != 1_000_000_000 || got.DataSize != 8 {
		t.Fatalf("entry1 wrong: %+v err=%v", got, err)
	}
	x.Close()

	// SHA mismatch must be rejected.
	other := make([]byte, 32)
	other[0] = 1
	if _, err := OpenIndex(path, other); !errors.Is(err, ErrIndexMismatch) {
		t.Fatalf("want mismatch, got %v", err)
	}
	// Tampered payload must fail checksum.
	f, _ := os.OpenFile(path, os.O_RDWR, 0o644)
	f.WriteAt([]byte{0xFF}, headerSize)
	f.Close()
	if _, err := OpenIndex(path, sha); !errors.Is(err, ErrIndexCorrupt) {
		t.Fatalf("want corrupt, got %v", err)
	}
	// A leftover .tmp must never be opened as an index.
	if _, err := OpenIndex(path+".tmp", sha); !os.IsNotExist(err) {
		t.Fatalf("temp should be absent/unusable: %v", err)
	}
}

func TestInterruptedBuildIsUnusable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idx")
	sha := make([]byte, 32)
	b, err := NewIndexBuilder(path, sha)
	if err != nil {
		t.Fatal(err)
	}
	_ = b.Append(IndexEntry{TrackNo: 1})
	// Simulate crash: close without Commit.
	b.tmp.Close()
	if _, err := OpenIndex(path, sha); !os.IsNotExist(err) {
		t.Fatalf("want no published index, got %v", err)
	}
	if _, err := os.Stat(path + ".tmp"); err != nil {
		t.Fatal("temp should remain for inspection")
	}
}
