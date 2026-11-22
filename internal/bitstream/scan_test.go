package bitstream

import (
	"testing"

	"mediq/internal/timeline"
)

// annexBUnit builds one synthetic H.264/H.265 NAL unit with a 4-byte
// start code, NAL header byte(s), MDIQ meta header and CRC'd payload.
// The NAL body (meta + payload) is emulation-prevention escaped so no
// start-code byte sequence appears inside it.
func annexBUnit(codec Codec, typ, seq uint32, pts timeline.Ticks, key bool, body []byte) []byte {
	hdr := []byte{byte(typ)}
	if codec == CodecH265 {
		hdr = []byte{byte(typ << 1), 1} // h265Type extracts bits 1..6
	}
	if key { // keyframes use the real RAP NAL type regardless of typ
		if codec == CodecH264 {
			hdr[0] = 5
		} else {
			hdr[0] = 19 << 1
		}
	}
	meta := EncodeMeta(seq, pts, uint32(len(body)), CRC32(body))
	inner := append(append([]byte{}, meta[:]...), body...)
	out := append([]byte{0, 0, 0, 1}, hdr...)
	return append(out, escapeEPB(inner)...)
}

// av1OBU builds one synthetic AV1 OBU: header (with obu_has_size set),
// LEB128 size, then a MDIQ meta header over [frameFlag | body].
func av1OBU(typ, seq uint32, pts timeline.Ticks, key bool, body []byte) []byte {
	framed := append([]byte{boolByte(key)}, body...)
	meta := EncodeMeta(seq, pts, uint32(len(framed)), CRC32(framed))
	payload := append(meta[:], framed...)
	return append([]byte{byte(typ<<3) | 0x02, byte(len(payload))}, payload...)
}

func boolByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}

func TestScanH264RoundTrip(t *testing.T) {
	var data []byte
	data = append(data, annexBUnit(CodecH264, 7, 0, 0, false, []byte("sps"))...)
	data = append(data, annexBUnit(CodecH264, 5, 1, 3000, true, []byte("idr"))...)
	data = append(data, annexBUnit(CodecH264, 1, 2, 6000, false, []byte("p"))...)
	res, err := Scan(CodecH264, data, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Units) != 3 {
		t.Fatalf("units = %d, want 3", len(res.Units))
	}
	if res.Truncated || len(res.Errors) != 0 {
		t.Fatalf("clean stream reported damage: trunc=%v errs=%v", res.Truncated, res.Errors)
	}
	kf := res.Keyframes()
	if len(kf) != 1 || kf[0].PTS != 3000 || kf[0].Type != 5 {
		t.Fatalf("keyframe = %+v, want single IDR at PTS 3000", kf)
	}
	if res.Units[0].Length+res.Units[1].Length+res.Units[2].Length != int64(len(data)) {
		t.Fatal("unit lengths must cover the whole stream")
	}
}

func TestScanH265RAPAndThreeByteCode(t *testing.T) {
	data := append(annexBUnit(CodecH265, 19, 0, 0, true, []byte("k")),
		annexBUnit(CodecH265, 1, 1, 3000, false, []byte("p"))...)
	res, err := Scan(CodecH265, data, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Keyframes()) != 1 || res.Keyframes()[0].Type != 19 {
		t.Fatalf("want one HEVC IDR type 19, got %+v", res.Units)
	}
	// Replace the 4-byte start code of the second unit with 00 00 01.
	cut := res.Units[1].Offset
	three := append([]byte{0, 0, 1}, data[cut+4:]...)
	data2 := append(append([]byte{}, data[:cut]...), three...)
	res2, err := Scan(CodecH265, data2, DefaultLimits())
	if err != nil || len(res2.Units) != 2 || len(res2.Keyframes()) != 1 {
		t.Fatalf("3-byte start code scan failed: err=%v units=%d", err, len(res2.Units))
	}
}

func TestScanTruncatedTail(t *testing.T) {
	good := append(annexBUnit(CodecH264, 5, 0, 0, true, []byte("idr")),
		annexBUnit(CodecH264, 1, 1, 3000, false, make([]byte, 64))...)
	res, err := Scan(CodecH264, dataWithCutTail(good, 12), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || len(res.Units) != 1 {
		t.Fatalf("want 1 complete unit + truncation, got units=%d trunc=%v", len(res.Units), res.Truncated)
	}
	if res.TruncOffset != res.Units[0].Offset+res.Units[0].Length {
		t.Fatalf("trunc offset %d must follow last complete unit", res.TruncOffset)
	}
	if res.CompleteBytes != res.Units[0].Offset+res.Units[0].Length {
		t.Fatal("CompleteBytes must exclude the incomplete tail")
	}
}

func dataWithCutTail(data []byte, drop int) []byte { return data[:len(data)-drop] }

func TestScanCorruptUnitIsQuarantinedButKept(t *testing.T) {
	first := annexBUnit(CodecH264, 5, 0, 0, true, []byte("idr"))
	data := append(first, annexBUnit(CodecH264, 1, 1, 3000, false, []byte("p"))...)
	// Flip the final payload byte of the first unit (inside its span only).
	data[len(first)-1] ^= 0xff
	res, err := Scan(CodecH264, data, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Units) != 2 || !res.Units[0].Corrupt || res.Units[1].Corrupt {
		t.Fatalf("want first unit corrupt only, got %+v", res.Units)
	}
	if len(res.Keyframes()) != 0 {
		t.Fatal("corrupt keyframe must not be offered as a usable RAP")
	}
	if len(res.Errors) != 1 || res.Errors[0].Kind != timeline.KindCorruptUnit {
		t.Fatalf("want one corrupt_unit error, got %v", res.Errors)
	}
}

func TestScanAV1KeyframesAndTruncation(t *testing.T) {
	var data []byte
	data = append(data, av1OBU(1, 0, 0, false, []byte("seq"))...)      // sequence header
	data = append(data, av1OBU(6, 1, 3000, true, []byte("key"))...)    // key frame
	data = append(data, av1OBU(6, 2, 6000, false, []byte("inter"))...) // inter
	res, err := Scan(CodecAV1, data, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Units) != 3 || len(res.Keyframes()) != 1 {
		t.Fatalf("av1 scan: units=%d keyframes=%d", len(res.Units), len(res.Keyframes()))
	}
	if res.Keyframes()[0].PTS != 3000 {
		t.Fatalf("av1 key PTS = %d, want 3000", res.Keyframes()[0].PTS)
	}
	trunc, err := Scan(CodecAV1, data[:len(data)-3], DefaultLimits())
	if err != nil || !trunc.Truncated || len(trunc.Units) != 2 {
		t.Fatalf("av1 truncation: err=%v trunc=%v units=%d", err, trunc.Truncated, len(trunc.Units))
	}
}

func TestScanQuotaEnforced(t *testing.T) {
	var data []byte
	for i := 0; i < 10; i++ {
		data = append(data, annexBUnit(CodecH264, 1, uint32(i), timeline.Ticks(i*3000), false, []byte("x"))...)
	}
	lim := DefaultLimits()
	lim.MaxUnits = 5
	if _, err := Scan(CodecH264, data, lim); err == nil {
		t.Fatal("want quota error when MaxUnits exceeded")
	}
	lim = DefaultLimits()
	lim.MaxInputBytes = 4
	if _, err := Scan(CodecH264, data, lim); err == nil {
		t.Fatal("want input-size quota error")
	}
}

func TestFindStartCodesAndLEB128(t *testing.T) {
	if got := findStartCodes([]byte{1, 0, 0, 1, 0, 0, 0, 1, 9}); len(got) != 2 || got[0] != 1 || got[1] != 4 {
		t.Fatalf("findStartCodes = %v", got)
	}
	if v, n, ok := readLEB128([]byte{0x80, 0x01}); !ok || v != 128 || n != 2 {
		t.Fatalf("leb128 = %d,%d,%v", v, n, ok)
	}
	if _, _, ok := readLEB128([]byte{0x80}); ok {
		t.Fatal("truncated LEB128 must fail")
	}
}

func TestEmulationPreventionRoundTrip(t *testing.T) {
	raw := []byte{0, 0, 0, 1, 9, 0, 0, 2, 0, 0, 3, 0, 0, 0, 0, 1, 7}
	esc := escapeEPB(raw)
	if got := findStartCodes(esc); len(got) != 0 {
		t.Fatalf("escaped body must contain no start codes, got %v in %x", got, esc)
	}
	if got := unescapeEPB(esc); string(got) != string(raw) {
		t.Fatalf("EPB round trip mismatch: %x != %x", got, raw)
	}
	// A NAL whose seq value contains 00 00 00 01 must stay one unit.
	u := annexBUnit(CodecH264, 1, 0x00000001, timeline.Ticks(0x00000100), false,
		[]byte{0, 0, 0, 1, 0, 0, 2})
	res, err := Scan(CodecH264, u, DefaultLimits())
	if err != nil || len(res.Units) != 1 || res.Units[0].Corrupt {
		t.Fatalf("EPB framing failed: err=%v units=%d corrupt=%v", err, len(res.Units), res != nil && res.Units[0].Corrupt)
	}
}
