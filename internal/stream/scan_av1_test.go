package stream

import (
	"os"
	"path/filepath"
	"testing"

	perr "gamepack/internal/errors"
)

// obu builds a length-delimited AV1 OBU: header + leb128(size) + payload.
func obu(typ uint8, payload []byte) []byte {
	h := byte(0x80) | (typ << 3) | 0x02 // obu_has_size_field=1
	out := []byte{h}
	out = appendLeb128(out, uint64(len(payload)))
	return append(out, payload...)
}

func appendLeb128(b []byte, v uint64) []byte {
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b = append(b, c|0x80)
		} else {
			return append(b, c)
		}
	}
}

func av1Stream() []byte {
	return concat(
		obu(2, []byte{0x00}),             // temporal delimiter
		obu(1, []byte{0x0a, 0x0b}),       // sequence header => keyframe anchor
		obu(6, []byte{0x11, 0x22, 0x33}), // frame OBU
	)
}

func TestScanAV1HappyPath(t *testing.T) {
	raw := av1Stream()
	// Sequence header starts at byte 2 (TD: 1 header + 1 len + 1 payload).
	idx := []byte(`{"codec":"av1","entries":[{"offset":3,"length":4}]}`)
	dir := t.TempDir()
	sp, ip := writeFiles(t, dir, "v", raw, idx)
	res, err := ScanFile(CodecAV1, sp, ip)
	if err != nil {
		t.Fatalf("scan av1: %v", err)
	}
	// Padding excluded; three real OBUs retained.
	if len(res.Units) != 3 {
		t.Fatalf("units = %d, want 3", len(res.Units))
	}
	if res.Boundaries[0].Offset != 3 {
		t.Fatalf("boundary offset = %d, want 3", res.Boundaries[0].Offset)
	}
}

func TestScanAV1TruncatedOBU(t *testing.T) {
	raw := append(obu(1, []byte{1, 2, 3}), 0x82, 0x05, 0xaa) // OBU claims 5, gives 1
	idx := []byte(`{"codec":"av1","entries":[{"offset":0,"length":5}]}`)
	dir := t.TempDir()
	sp, ip := writeFiles(t, dir, "v", raw, idx)
	_, err := ScanFile(CodecAV1, sp, ip)
	pe := perr.AsError(err)
	if pe == nil || pe.Code != perr.CodeTruncatedUnit {
		t.Fatalf("want TRUNCATED_UNIT, got %v", err)
	}
}

func TestScanAV1MissingSizeField(t *testing.T) {
	raw := []byte{0x80 | (6 << 3), 0xaa, 0xbb} // no size field
	idx := []byte(`{"codec":"av1","entries":[{"offset":0,"length":2}]}`)
	dir := t.TempDir()
	sp, ip := writeFiles(t, dir, "v", raw, idx)
	_, err := ScanFile(CodecAV1, sp, ip)
	pe := perr.AsError(err)
	if pe == nil || pe.Code != perr.CodeInvalidEncoding {
		t.Fatalf("want INVALID_ENCODING, got %v", err)
	}
}

func TestReconcileDuplicateEntry(t *testing.T) {
	raw := h264Stream()
	idx := []byte(`{"codec":"h264","entries":[{"offset":15,"length":3},{"offset":15,"length":3}]}`)
	dir := t.TempDir()
	sp, ip := writeFiles(t, dir, "v", raw, idx)
	_, err := ScanFile(CodecH264, sp, ip)
	pe := perr.AsError(err)
	if pe == nil || pe.Code != perr.CodeIndexMissing || pe.Offset != 15 {
		t.Fatalf("want duplicate INDEX_MISSING at 15, got %v", err)
	}
}

func TestReconcileNonBoundaryOffset(t *testing.T) {
	raw := h264Stream()
	// Offset 16 is inside the IDR NAL, not a unit start.
	idx := []byte(`{"codec":"h264","entries":[{"offset":16,"length":2}]}`)
	dir := t.TempDir()
	sp, ip := writeFiles(t, dir, "v", raw, idx)
	_, err := ScanFile(CodecH264, sp, ip)
	pe := perr.AsError(err)
	if pe == nil || pe.Code != perr.CodeIndexMissing {
		t.Fatalf("want INDEX_MISSING, got %v", err)
	}
}

func TestMissingIndexFile(t *testing.T) {
	dir := t.TempDir()
	sp := filepath.Join(dir, "v.bits")
	if err := os.WriteFile(sp, h264Stream(), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ScanFile(CodecH264, sp, filepath.Join(dir, "nope.json"))
	pe := perr.AsError(err)
	if pe == nil || pe.Code != perr.CodeIndexMissing {
		t.Fatalf("want INDEX_MISSING, got %v", err)
	}
}
