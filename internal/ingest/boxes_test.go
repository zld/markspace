package ingest

import (
	"encoding/binary"
	"errors"
	"testing"
)

// makeBox builds a well-formed box with the given type and payload length.
func makeBox(typ string, payloadLen int) []byte {
	b := make([]byte, 8+payloadLen)
	binary.BigEndian.PutUint32(b[0:4], uint32(len(b)))
	copy(b[4:8], typ)
	return b
}

// makeMoofWithSamples builds a minimal moof that sizeSane accepts as a resync
// anchor (the scanner only needs a plausible size and type).
func makeMoofWithSamples() []byte {
	return makeBox("moof", 16)
}

func TestReadBoxHeaderAtValid(t *testing.T) {
	b := makeBox("ftyp", 16)
	h, err := ReadBoxHeaderAt(b, 0, uint64(len(b)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.Type != "ftyp" || h.Size != 24 || h.HeaderSize != 8 {
		t.Fatalf("bad header: %+v", h)
	}
	if h.PayloadOffset() != 8 || h.End() != 24 {
		t.Fatalf("bad offsets: %+v", h)
	}
}

func TestReadBoxHeaderAtTruncatedPayload(t *testing.T) {
	b := makeBox("mdat", 100)
	// Pretend only 50 bytes of the file exist.
	_, err := ReadBoxHeaderAt(b, 0, 50)
	if !errors.Is(err, ErrTruncatedPayload) {
		t.Fatalf("want ErrTruncatedPayload, got %v", err)
	}
	var be *BoxError
	if !errors.As(err, &be) {
		t.Fatal("expected BoxError")
	}
	if be.Declared != 108 || be.Available != 50 {
		t.Fatalf("bad BoxError numbers: %+v", be)
	}
}

func TestReadBoxHeaderAtIncompleteHeader(t *testing.T) {
	b := []byte{0, 0, 0, 1, 'm', 'o'}
	_, err := ReadBoxHeaderAt(b, 0, uint64(len(b)))
	if !errors.Is(err, ErrIncompleteHeader) {
		t.Fatalf("want ErrIncompleteHeader, got %v", err)
	}
}

func TestReadBoxHeaderAtInvalidSize(t *testing.T) {
	b := make([]byte, 8)
	binary.BigEndian.PutUint32(b[0:4], 4) // smaller than the header
	copy(b[4:8], "free")
	_, err := ReadBoxHeaderAt(b, 0, uint64(len(b)))
	if !errors.Is(err, ErrInvalidSize) {
		t.Fatalf("want ErrInvalidSize, got %v", err)
	}
}

func TestReadBoxHeaderAtLargeSize(t *testing.T) {
	// 16-byte header with a 64-bit size.
	b := make([]byte, 32)
	binary.BigEndian.PutUint32(b[0:4], 1)
	copy(b[4:8], "mdat")
	binary.BigEndian.PutUint64(b[8:16], 32)
	h, err := ReadBoxHeaderAt(b, 0, uint64(len(b)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.HeaderSize != 16 || h.Size != 32 {
		t.Fatalf("bad large header: %+v", h)
	}
}

func TestScanRecoversAfterTruncatedMdat(t *testing.T) {
	// ftyp | moof | mdat (declares too much) | moof | mdat (valid)
	var data []byte
	data = append(data, makeBox("ftyp", 8)...)
	data = append(data, makeMoofWithSamples()...)
	badMdat := makeBox("mdat", 32)
	binary.BigEndian.PutUint32(badMdat[0:4], 60000) // declares far more than exist
	data = append(data, badMdat...)
	data = append(data, makeMoofWithSamples()...)
	data = append(data, makeBox("mdat", 32)...)

	items, stats := Scan(data, 1<<20)
	if stats.Truncated != 1 {
		t.Fatalf("want 1 truncated box, got %d", stats.Truncated)
	}
	if stats.TrailingBytes != 0 {
		t.Fatalf("damage should be recoverable, trailing=%d", stats.TrailingBytes)
	}
	var boxes, damages int
	var recovered bool
	for _, it := range items {
		if it.Box != nil {
			boxes++
		} else {
			damages++
			if it.Damage.Recovered {
				recovered = true
			}
		}
	}
	if damages != 1 || !recovered {
		t.Fatalf("want 1 recovered damage, got damages=%d recovered=%v", damages, recovered)
	}
	if boxes != 4 {
		t.Fatalf("want 4 valid boxes (ftyp, moof, moof, mdat), got %d", boxes)
	}
}

func TestScanUnrecoverableTrailing(t *testing.T) {
	data := makeBox("ftyp", 8)
	// Append bytes that cannot form a box and contain no moof anchor.
	data = append(data, 0xDE, 0xAD, 0xBE, 0xEF, 0x00, 0x00, 0x00, 0x10)
	_, stats := Scan(data, 1<<20)
	if stats.TrailingBytes == 0 {
		t.Fatal("expected unrecoverable trailing bytes")
	}
}

func TestResyncMoofRejectsTruncated(t *testing.T) {
	// A moof header whose declared size exceeds the input must not resync.
	b := makeMoofWithSamples()
	binary.BigEndian.PutUint32(b[0:4], 1<<20)
	off, ok := ResyncMoof(b, 0, 1<<24)
	if ok {
		t.Fatalf("resynced to truncated moof at %d", off)
	}
}

func TestScanRespectsMaxBoxSize(t *testing.T) {
	data := makeBox("mdat", 4096)
	data = append(data, makeMoofWithSamples()...)
	data = append(data, makeBox("mdat", 16)...)
	items, stats := Scan(data, 1024)
	if stats.Overflow != 1 {
		t.Fatalf("want 1 overflow, got %d", stats.Overflow)
	}
	var sawMoof bool
	for _, it := range items {
		if it.Box != nil && it.Box.Header.Type == "moof" {
			sawMoof = true
		}
	}
	if !sawMoof {
		t.Fatal("scanner should have resynchronised onto the following moof")
	}
}

func TestScanDamageBetweenMoofAndMdat(t *testing.T) {
	// ftyp | moof | <garbage that is not a box> | mdat
	var data []byte
	data = append(data, makeBox("ftyp", 8)...)
	moofStart := len(data)
	data = append(data, makeMoofWithSamples()...)
	// Corrupt a small region right after the moof: 4 bytes of nonsense whose
	// declared size (0xDEADBEEF) cannot fit, forcing a damage item.
	data = append(data, 0xDE, 0xAD, 0xBE, 0xEF, 'j', 'u', 'n', 'k')
	data = append(data, makeBox("mdat", 32)...)

	items, stats := Scan(data, 1<<20)
	if stats.Truncated == 0 && stats.Overflow == 0 && stats.Invalid == 0 {
		t.Fatalf("expected the junk region to be flagged: %+v", stats)
	}
	// The moof before the junk must still appear as a box so the reader can
	// discard it explicitly rather than misattributing the later mdat.
	foundMoof := false
	sawDamage := false
	for _, it := range items {
		if it.Box != nil && it.Box.Header.Type == "moof" && int(it.Box.Header.Offset) == moofStart {
			foundMoof = true
		}
		if it.Damage != nil {
			sawDamage = true
		}
	}
	if !foundMoof {
		t.Fatal("moof before the damaged region was lost")
	}
	if !sawDamage {
		t.Fatal("damaged region was not isolated")
	}
}
