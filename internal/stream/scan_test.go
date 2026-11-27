package stream

import (
	"os"
	"path/filepath"
	"testing"

	perr "gamepack/internal/errors"
)

func writeFiles(t *testing.T, dir, streamName string, stream []byte, idx []byte) (string, string) {
	t.Helper()
	sp := filepath.Join(dir, streamName+".bits")
	ip := filepath.Join(dir, streamName+".idx.json")
	if err := os.WriteFile(sp, stream, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ip, idx, 0o600); err != nil {
		t.Fatal(err)
	}
	return sp, ip
}

func h264Stream() []byte {
	// SPS (type 7), P-slice (type 1), IDR (type 5), P-slice (type 1).
	return concat(
		startCode(4), []byte{0x67, 0x42, 0x00}, // SPS
		startCode(3), []byte{0x41, 0xaa},       // non-IDR
		startCode(3), []byte{0x65, 0xbb, 0xcc}, // IDR keyframe
		startCode(3), []byte{0x41, 0xdd},       // non-IDR
	)
}

func h264IndexGood() []byte {
	// IDR starts at offset of third start code: 4+3 + 3+2 + 3 = 15.
	return []byte(`{"codec":"h264","entries":[{"offset":15,"length":3}]}`)
}

func startCode(n int) []byte {
	if n == 4 {
		return []byte{0, 0, 0, 1}
	}
	return []byte{0, 0, 1}
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestScanAnnexBHappyPath(t *testing.T) {
	sp, ip := writeFiles(t, t.TempDir(), "v", h264Stream(), h264IndexGood())
	res, err := ScanFile(CodecH264, sp, ip)
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}
	if len(res.Units) != 4 {
		t.Fatalf("units = %d, want 4", len(res.Units))
	}
	if !res.Units[2].Key || res.Units[2].Type != 5 {
		t.Fatalf("unit 2 should be IDR keyframe: %+v", res.Units[2])
	}
	if len(res.Boundaries) != 1 || res.Boundaries[0].Offset != 15 {
		t.Fatalf("boundaries = %+v", res.Boundaries)
	}
	if err := ValidateBoundaries(res); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestScanOffsetOutOfRange(t *testing.T) {
	bad := []byte(`{"codec":"h264","entries":[{"offset":9999,"length":3}]}`)
	sp, ip := writeFiles(t, t.TempDir(), "v", h264Stream(), bad)
	_, err := ScanFile(CodecH264, sp, ip)
	pe := perr.AsError(err)
	if pe == nil || pe.Code != perr.CodeOffsetOutOfRange || pe.Offset != 9999 {
		t.Fatalf("want OFFSET_OUT_OF_RANGE at 9999, got %v", err)
	}
}

func TestScanTruncatedUnit(t *testing.T) {
	raw := h264Stream()
	// Index declares a length that runs past EOF.
	bad := []byte(`{"codec":"h264","entries":[{"offset":15,"length":50}]}`)
	sp, ip := writeFiles(t, t.TempDir(), "v", raw, bad)
	_, err := ScanFile(CodecH264, sp, ip)
	pe := perr.AsError(err)
	if pe == nil || pe.Code != perr.CodeTruncatedUnit || pe.Offset != 15 {
		t.Fatalf("want TRUNCATED_UNIT at 15, got %v", err)
	}
}

func TestScanIndexMissingKeyframe(t *testing.T) {
	// Empty index while the stream contains an IDR: must report missing entry.
	bad := []byte(`{"codec":"h264","entries":[]}`)
	sp, ip := writeFiles(t, t.TempDir(), "v", h264Stream(), bad)
	_, err := ScanFile(CodecH264, sp, ip)
	pe := perr.AsError(err)
	if pe == nil || pe.Code != perr.CodeIndexMissing {
		t.Fatalf("want INDEX_MISSING, got %v", err)
	}
}

func TestScanTruncatedAnnexBPrefix(t *testing.T) {
	// Stream ends inside a start code: no complete unit may be emitted.
	raw := append(h264Stream(), 0, 0)
	sp, ip := writeFiles(t, t.TempDir(), "v", raw, h264IndexGood())
	if _, err := ScanFile(CodecH264, sp, ip); err == nil {
		t.Fatal("trailing partial start code must fail")
	}
}

func TestScanNoStartCode(t *testing.T) {
	raw := []byte{0x42, 0x43, 0x44}
	sp, ip := writeFiles(t, t.TempDir(), "v", raw, h264IndexGood())
	_, err := ScanFile(CodecH264, sp, ip)
	pe := perr.AsError(err)
	if pe == nil || pe.Code != perr.CodeInvalidEncoding || pe.Offset != 0 {
		t.Fatalf("want INVALID_ENCODING at byte 0, got %v", err)
	}
}
