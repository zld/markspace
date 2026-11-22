package codec

import (
	"bytes"
	"testing"
)

func annexB(nals ...[]byte) []byte {
	var out []byte
	for _, n := range nals {
		out = append(out, 0, 0, 0, 1)
		out = append(out, n...)
	}
	return out
}

func TestScanH264FindsUnitsAndStartCodeLengths(t *testing.T) {
	sps := []byte{0x67, 0x42, 0x00, 0x1E}
	pps := []byte{0x68, 0xCE, 0x06, 0xE2}
	idr := []byte{0x65, 0x88, 0x84, 0x00}
	nonIDR := []byte{0x41, 0x9A, 0x02, 0x01}
	buf := append([]byte{}, annexB(sps, pps, idr)...)
	// Use a three-byte start code before the non-IDR slice. The trailing
	// NAL must not end in 0x00, or it would read as trailing_zero_8bits.
	nonIDR = []byte{0x41, 0x9A, 0x02, 0x03}
	buf = append(buf, 0, 0, 1)
	buf = append(buf, nonIDR...)

	res := ScanH264(0, buf, DefaultScanLimits())
	if len(res.Units) != 4 {
		t.Fatalf("units %d want 4", len(res.Units))
	}
	if len(res.Damaged) != 0 {
		t.Fatalf("unexpected damage %+v", res.Damaged)
	}
	// FindStartCodes reports the first zero of a run; a 00000001 run is read
	// as a four-byte start code even when written as 000001 after a NAL that
	// ended in zero. Both forms are legal Annex B.
	if res.Units[0].HeaderLen != 4 {
		t.Fatalf("first start code length %d", res.Units[0].HeaderLen)
	}
	if len(res.Keyframes) != 1 || res.Units[res.Keyframes[0]].Type != 5 {
		t.Fatalf("keyframes %v", res.Keyframes)
	}
	// Only VCL NALs are frame starts; SPS and PPS are not pictures.
	if res.Units[0].FrameStart || !res.Units[2].FrameStart {
		t.Fatalf("frame start flags: %+v", res.Units)
	}
}

func TestScanH264TruncationIsolated(t *testing.T) {
	good := annexB([]byte{0x67, 0x42, 0x00, 0x1E})
	// A unit whose declared size exceeds the buffer is not expressible in
	// Annex B (there are no lengths), so truncation appears as a dangling
	// start code or a cut tail. Both must be isolated.
	buf := append([]byte{}, good...)
	buf = append(buf, 0, 0, 1) // start code with nothing after it
	res := ScanH264(0, buf, DefaultScanLimits())
	if len(res.Units) != 1 {
		t.Fatalf("units %d want 1", len(res.Units))
	}
	if len(res.Damaged) != 1 {
		t.Fatalf("damage %+v", res.Damaged)
	}
	if res.Damaged[0].Offset != int64(len(good)) {
		t.Fatalf("damage offset %d", res.Damaged[0].Offset)
	}
}

func TestScanH264LeadingGarbage(t *testing.T) {
	buf := append([]byte{0xDE, 0xAD}, annexB([]byte{0x67, 0x42})...)
	res := ScanH264(0, buf, DefaultScanLimits())
	if len(res.Damaged) != 1 || res.Damaged[0].Offset != 0 || res.Damaged[0].Length != 2 {
		t.Fatalf("damage %+v", res.Damaged)
	}
	if len(res.Units) != 1 {
		t.Fatalf("units %d", len(res.Units))
	}
}

func TestScanH265KeyframeTypes(t *testing.T) {
	vps := []byte{0x40, 0x01, 0x0C}
	idr := []byte{0x26, 0x01, 0xAF}
	trail := []byte{0x02, 0x01, 0x55}
	buf := annexB(vps, idr, trail)
	res := ScanH265(0, buf, DefaultScanLimits())
	if len(res.Units) != 3 {
		t.Fatalf("units %d", len(res.Units))
	}
	if res.Units[0].Type != 32 || res.Units[0].Keyframe {
		t.Fatalf("VPS must not be a keyframe: %+v", res.Units[0])
	}
	if res.Units[1].Type != 19 || !res.Units[1].Keyframe {
		t.Fatalf("IDR_W_RADL must be a keyframe: %+v", res.Units[1])
	}
	if res.Units[2].Keyframe {
		t.Fatalf("TRAIL_R must not be a keyframe")
	}
}

func TestCanonicalExcludesStartCodes(t *testing.T) {
	nal1 := []byte{0x67, 0x42, 0x00}
	nal2 := []byte{0x65, 0x01}
	data := annexB(nal1, nal2)
	res := ScanH264(0, data, DefaultScanLimits())
	canon := Canonical(res.Units, data)
	want := append(append([]byte{}, nal1...), nal2...)
	if !bytes.Equal(canon, want) {
		t.Fatalf("canonical %x want %x", canon, want)
	}
}

func TestLengthPrefixedRoundTrip(t *testing.T) {
	nal1 := []byte{0x67, 0x42, 0x00, 0x1E}
	nal2 := []byte{0x65, 0x01, 0x02}
	data := annexB(nal1, nal2)
	res := ScanH264(0, data, DefaultScanLimits())
	canon := Canonical(res.Units, data)

	// Build length-prefixed storage and read it back.
	var stored []byte
	for _, u := range res.Units {
		start := u.Offset + u.HeaderLen
		end := start + u.Size
		n := uint32(end - start)
		stored = append(stored, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
		stored = append(stored, data[start:end]...)
	}
	back, damaged := LengthPrefixedCanonical(stored, DefaultScanLimits())
	if len(damaged) != 0 {
		t.Fatalf("damage %+v", damaged)
	}
	if !bytes.Equal(back, canon) {
		t.Fatalf("round trip changed media: %x vs %x", back, canon)
	}
}

func TestWrapLengthPrefixedToAnnexB(t *testing.T) {
	// WrapLengthPrefixed converts MP4-style length-prefixed bytes to Annex B
	// without touching NAL payloads.
	nal := []byte{0x65, 0x88, 0x84}
	stored := []byte{0, 0, 0, byte(len(nal))}
	stored = append(stored, nal...)
	annexed, damaged := WrapLengthPrefixed(0, stored, DefaultScanLimits())
	if len(damaged) != 0 {
		t.Fatalf("damage %+v", damaged)
	}
	if !bytes.Equal(annexed, annexB(nal)) {
		t.Fatalf("annex b output %x want %x", annexed, annexB(nal))
	}
	// A truncated length prefix is reported but earlier NALs survive.
	truncated := append(append([]byte{}, stored...), 0, 0)
	annexed, damaged = WrapLengthPrefixed(0, truncated, DefaultScanLimits())
	if len(damaged) != 1 {
		t.Fatalf("truncated damage %+v", damaged)
	}
	if !bytes.Equal(annexed, annexB(nal)) {
		t.Fatalf("valid prefix before truncation was lost")
	}
}

// av1OBU builds one length-delimited OBU.
func av1OBU(typ byte, payload []byte) []byte {
	out := []byte{typ << 3 | 0x02, byte(len(payload))}
	return append(out, payload...)
}

func TestScanAV1FrameTypes(t *testing.T) {
	key := []byte{0x00, 0x11, 0x22}
	inter := []byte{0x20, 0x33}
	buf := append(av1OBU(6, key), av1OBU(6, inter)...)
	res := ScanAV1(0, buf, DefaultScanLimits())
	if len(res.Units) != 2 || len(res.Damaged) != 0 {
		t.Fatalf("units %d damage %+v", len(res.Units), res.Damaged)
	}
	if !res.Units[0].Keyframe || res.Units[1].Keyframe {
		t.Fatalf("keyframe classification %+v", res.Units)
	}
	if res.Units[0].FrameType != AV1KeyFrame || res.Units[1].FrameType != AV1InterFrame {
		t.Fatalf("frame types %d %d", res.Units[0].FrameType, res.Units[1].FrameType)
	}
}

func TestScanAV1TruncatedResyncs(t *testing.T) {
	// The first OBU declares 200 bytes, which runs past the buffer. The
	// scanner reports that truncation; a later OBU inside the declared range
	// is not independently addressable because OBU framing has no start code,
	// so recovery starts only after the truncated span ends.
	bad := []byte{6<<3 | 0x02, 0xC8, 0x01, 0x02}
	res := ScanAV1(0, bad, DefaultScanLimits())
	if len(res.Damaged) == 0 {
		t.Fatal("truncated OBU was not reported")
	}
	if res.Damaged[0].Offset != 0 {
		t.Fatalf("damage offset %d", res.Damaged[0].Offset)
	}
	// Scanning resumes cleanly for the next sample, which is the recovery
	// boundary that matters to the pipeline.
	good := av1OBU(6, []byte{0x00, 0x44})
	res2 := ScanAV1(0, good, DefaultScanLimits())
	if len(res2.Units) != 1 || !res2.Units[0].Keyframe {
		t.Fatalf("next sample did not scan cleanly: %+v", res2.Units)
	}
}

func TestScanAV1ForbiddenBit(t *testing.T) {
	buf := []byte{0x80, 0x01, 0x00}
	res := ScanAV1(0, buf, DefaultScanLimits())
	if len(res.Errors) == 0 || res.Errors[0].Code != "obu_forbidden_bit" {
		t.Fatalf("errors %+v", res.Errors)
	}
}

func TestScanAV1SequenceHeaderIsNotKeyframe(t *testing.T) {
	// A sequence header alone must not count as a keyframe anchor.
	buf := av1OBU(1, []byte{0x00, 0x01})
	res := ScanAV1(0, buf, DefaultScanLimits())
	if len(res.Units) != 1 || res.Units[0].Keyframe {
		t.Fatalf("sequence header keyframe flag: %+v", res.Units)
	}
}

func TestScanLimitsRejectOversize(t *testing.T) {
	buf := av1OBU(6, make([]byte, 32))
	res := ScanAV1(0, buf, ScanLimits{MaxUnits: 16, MaxUnitBytes: 8})
	if len(res.Errors) == 0 {
		t.Fatal("oversize OBU must be reported")
	}
}
