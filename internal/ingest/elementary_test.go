package ingest

import (
	"encoding/binary"
	"testing"

	"mediapipeline/internal/fault"
)

func h264NAL(nalType byte, payload []byte) []byte {
	b := make([]byte, 4+1+len(payload))
	binary.BigEndian.PutUint32(b, uint32(1+len(payload)))
	b[4] = nalType
	copy(b[5:], payload)
	return b
}

func TestScanH264HappyAndTruncation(t *testing.T) {
	stream := append(h264NAL(5, []byte{1, 2, 3}), h264NAL(1, []byte{9})...)
	var rep fault.Report
	units, err := Scan(CodecH264, stream, DefaultLimits, &rep)
	if err != nil || rep.Count() != 0 {
		t.Fatalf("unexpected fault: %v %v", err, rep.All())
	}
	if len(units) != 2 || !units[0].IsKey || units[0].Type != 5 {
		t.Fatalf("bad units: %+v", units)
	}

	// Truncate the second unit's declared payload by one byte.
	cut := append(append([]byte{}, h264NAL(5, []byte{1, 2, 3})...),
		h264NAL(1, []byte{9})[:4+1]...)
	var rep2 fault.Report
	units2, _ := Scan(CodecH264, cut, DefaultLimits, &rep2)
	if len(units2) != 1 {
		t.Fatalf("first unit must still parse, got %d", len(units2))
	}
	if !rep2.Has(fault.CodeNALTruncated) {
		t.Fatalf("expected truncation, got %v", rep2.All())
	}
	f := rep2.All()[0]
	want := int64(len(h264NAL(5, []byte{1, 2, 3})))
	if f.Offset != want {
		t.Fatalf("truncation offset %d, want %d", f.Offset, want)
	}
}

func TestScanIllegalLengthResumes(t *testing.T) {
	bad := make([]byte, 4)
	binary.BigEndian.PutUint32(bad, 0) // zero length
	stream := append(append(bad, h264NAL(1, []byte{7})...), h264NAL(5, []byte{8})...)
	var rep fault.Report
	units, _ := Scan(CodecH264, stream, DefaultLimits, &rep)
	if !rep.Has(fault.CodeNALIllegalLength) {
		t.Fatal("zero length must be illegal")
	}
	if len(units) != 2 || !units[1].IsKey {
		t.Fatalf("scanner must recover after illegal unit, got %+v", units)
	}

	// Trailing bytes shorter than a prefix are reported, not panicked on.
	var rep2 fault.Report
	if _, err := Scan(CodecH264, []byte{0, 1}, DefaultLimits, &rep2); err != nil {
		t.Fatal(err)
	}
	if !rep2.Has(fault.CodeNALTruncated) {
		t.Fatal("trailing fragment must be reported")
	}
}

func leb128(v uint64) []byte {
	var out []byte
	for {
		b := byte(v & 0x7F)
		v >>= 7
		if v != 0 {
			out = append(out, b|0x80)
		} else {
			return append(out, b)
		}
	}
}

func av1OBU(typ byte, payload []byte) []byte {
	header := byte(typ<<3) | 0x02 // obu_has_size_field
	out := []byte{header}
	out = append(out, leb128(uint64(len(payload)))...)
	return append(out, payload...)
}

func TestScanAV1KeyframesAndTruncation(t *testing.T) {
	stream := append(av1OBU(obuSequenceHeader, []byte{0xAA}),
		av1OBU(obuFrame, []byte{1, 2})...)
	var rep fault.Report
	units, err := Scan(CodecAV1, stream, DefaultLimits, &rep)
	if err != nil || rep.Count() != 0 {
		t.Fatalf("unexpected: %v %v", err, rep.All())
	}
	if len(units) != 2 || !units[0].IsKey || units[1].IsKey {
		t.Fatalf("bad AV1 units: %+v", units)
	}

	// Cut mid size field so leb128 runs past EOF.
	cut := append([]byte{}, stream...)
	cut = append(cut[:len(stream)-2], 0x80) // dangling continuation
	var rep2 fault.Report
	units2, _ := Scan(CodecAV1, cut, DefaultLimits, &rep2)
	if !rep2.Has(fault.CodeOBUTruncated) {
		t.Fatalf("expected OBU truncation, got %v", rep2.All())
	}
	// Only the first OBU fully precedes the corrupted framing point.
	if len(units2) != 1 {
		t.Fatalf("units before the cut must survive, got %d", len(units2))
	}

	// Forbidden header bit must be flagged with its exact offset.
	var rep3 fault.Report
	Scan(CodecAV1, []byte{0x80}, DefaultLimits, &rep3)
	if !rep3.Has(fault.CodeOBUIllegalLength) || rep3.All()[0].Offset != 0 {
		t.Fatalf("forbidden bit not located: %v", rep3.All())
	}
}

func TestScanLimitEnforced(t *testing.T) {
	stream := h264NAL(1, make([]byte, 1024))
	lim := DefaultLimits
	lim.MaxUnitBytes = 16
	var rep fault.Report
	units, _ := Scan(CodecH264, stream, lim, &rep)
	if !rep.Has(fault.CodeNALIllegalLength) || rep.All()[0].Offset != 0 {
		t.Fatalf("oversized unit must be rejected at its offset: %v", rep.All())
	}
	if len(units) != 0 {
		t.Fatalf("rejected unit must not enter output, got %d", len(units))
	}
}
