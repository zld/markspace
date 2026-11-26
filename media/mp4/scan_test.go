package mp4

import (
	"encoding/binary"
	"testing"

	"replaykit/media/fault"
)

// boxBytes builds a box header with the given size field and type.
func boxBytes(size uint32, typ string) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint32(b, size)
	copy(b[4:], typ)
	return b
}

func largeBoxBytes(size uint64, typ string) []byte {
	b := make([]byte, 16)
	binary.BigEndian.PutUint32(b, 1)
	copy(b[4:], typ)
	binary.BigEndian.PutUint64(b[8:], size)
	return b
}

func join(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func hasCategory(s *Scan, cat fault.Category) bool {
	for _, a := range s.Anomalies {
		if a.Category == cat {
			return true
		}
	}
	return false
}

func TestScanNestedBoxes(t *testing.T) {
	child := join(boxBytes(16, "mvhd"), make([]byte, 8))
	moov := join(boxBytes(uint32(8+len(child)), "moov"), child)
	data := join(boxBytes(uint32(8+len(moov)), "moov"), moov)

	s, err := ScanBytes(data, DefaultLimits())
	if err != nil {
		t.Fatalf("ScanBytes: %v", err)
	}
	if len(s.TopLevel) != 1 || s.TopLevel[0].Type != "moov" {
		t.Fatalf("top level = %+v", s.TopLevel)
	}
	ref, ok := s.First("mvhd")
	if !ok {
		t.Fatal("mvhd missing")
	}
	if ref.Offset != 16 || ref.Size != 16 || ref.Path != "moov/moov/mvhd" {
		t.Fatalf("ref = %+v", ref)
	}
	if len(s.Anomalies) != 0 {
		t.Fatalf("anomalies = %+v", s.Anomalies)
	}
}

func TestScanExtendsToEOF(t *testing.T) {
	data := join(boxBytes(0, "mdat"), make([]byte, 24))
	s, err := ScanBytes(data, DefaultLimits())
	if err != nil {
		t.Fatalf("ScanBytes: %v", err)
	}
	ref, _ := s.First("mdat")
	if ref.Size != 32 {
		t.Fatalf("size = %d", ref.Size)
	}
}

func TestScanLargeSize(t *testing.T) {
	data := join(largeBoxBytes(24, "mdat"), make([]byte, 8))
	s, err := ScanBytes(data, DefaultLimits())
	if err != nil {
		t.Fatalf("ScanBytes: %v", err)
	}
	ref, _ := s.First("mdat")
	if ref.HeaderSize != 16 || ref.Size != 24 || ref.PayloadSize() != 8 {
		t.Fatalf("ref = %+v", ref)
	}
}

func TestScanTruncatedBoxReportsOffset(t *testing.T) {
	data := join(boxBytes(8, "free"), boxBytes(64, "mdat"), make([]byte, 8))
	s, err := ScanBytes(data, DefaultLimits())
	if err != nil {
		t.Fatalf("structural defect must be recoverable, got %v", err)
	}
	if s == nil || !hasCategory(s, fault.CategoryTruncatedBox) {
		t.Fatalf("expected truncated anomaly, got %+v", s)
	}
	found := false
	for _, a := range s.Anomalies {
		if a.Category == fault.CategoryTruncatedBox {
			if a.Offset != 8 {
				t.Fatalf("offset = %d", a.Offset)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no truncated anomaly")
	}
}

func TestScanBoxOverflowLimit(t *testing.T) {
	limits := Limits{MaxFileBytes: 1024, MaxBoxBytes: 128}
	data := join(boxBytes(8, "free"), boxBytes(200, "mdat"), make([]byte, 192))
	s, err := ScanBytes(data, limits)
	if err != nil {
		t.Fatalf("limiting is an anomaly, not a fatal scan error: %v", err)
	}
	if !hasCategory(s, fault.CategoryBoxOverflow) {
		t.Fatalf("expected overflow anomaly, %+v", s.Anomalies)
	}
}

func TestScanMalformedSizeBelowHeader(t *testing.T) {
	data := join(boxBytes(8, "free"), boxBytes(4, "mdat"), make([]byte, 8))
	s, err := ScanBytes(data, DefaultLimits())
	if err != nil {
		t.Fatalf("malformed length is an anomaly: %v", err)
	}
	if !hasCategory(s, fault.CategoryMalformedBox) {
		t.Fatalf("anomalies = %+v", s.Anomalies)
	}
}

func TestScanTrailingBytes(t *testing.T) {
	data := join(boxBytes(8, "free"), []byte{1, 2, 3})
	s, err := ScanBytes(data, DefaultLimits())
	if err != nil {
		t.Fatalf("trailing bytes are an anomaly: %v", err)
	}
	if !hasCategory(s, fault.CategoryTruncatedBox) {
		t.Fatalf("anomalies = %+v", s.Anomalies)
	}
}

func TestScanEmptyInput(t *testing.T) {
	if _, err := ScanBytes(nil, DefaultLimits()); !fault.IsFatal(err) {
		t.Fatalf("expected fatal, got %v", err)
	}
}

func TestLimitsValidate(t *testing.T) {
	if err := DefaultLimits().Validate(); err != nil {
		t.Fatalf("default limits: %v", err)
	}
	if err := (Limits{MaxFileBytes: 100, MaxBoxBytes: 200}).Validate(); err == nil {
		t.Fatal("box > file should fail")
	}
	if err := (Limits{MaxFileBytes: 0, MaxBoxBytes: 1}).Validate(); err == nil {
		t.Fatal("zero file should fail")
	}
}

func TestPayloadBounds(t *testing.T) {
	data := join(boxBytes(8, "free"))
	s, _ := ScanBytes(data, DefaultLimits())
	ref, _ := s.First("free")
	if _, err := s.Payload(data, ref); err != nil {
		t.Fatalf("payload: %v", err)
	}
	bad := BoxRef{Type: "free", Offset: 4, Size: 16, HeaderSize: 8}
	if _, err := s.Payload(data, bad); err == nil {
		t.Fatal("expected bounds error")
	}
}

func TestScanFileMissing(t *testing.T) {
	if _, err := ScanFile("/nonexistent/replaykit.mp4", DefaultLimits()); !fault.IsFatal(err) {
		t.Fatalf("expected fatal, got %v", err)
	}
}
