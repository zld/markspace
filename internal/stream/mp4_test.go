package stream

import (
	"testing"

	"mediascan/internal/fault"
	"mediascan/internal/fixture"
)

func buildSegment(t *testing.T) []byte {
	t.Helper()
	samples := []fixture.Sample{
		{Duration: 100, Size: 24, Keyframe: true, Payload: 10},
		{Duration: 100, Size: 18, Keyframe: false, Payload: 20},
		{Duration: 100, Size: 30, Keyframe: true, Payload: 30},
	}
	return fixture.MediaSegment(1, 42, 5000, samples)
}

func TestParseMP4Clean(t *testing.T) {
	raw := buildSegment(t)
	res, err := ParseMP4(raw)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if res.MediaSeq != 42 || res.BaseDTS != 5000 {
		t.Fatalf("seq/dts wrong: seq=%d base=%d", res.MediaSeq, res.BaseDTS)
	}
	if len(res.Samples) != 3 {
		t.Fatalf("samples = %d, want 3", len(res.Samples))
	}
	wantKey := []bool{true, false, true}
	for i, s := range res.Samples {
		if s.Keyframe != wantKey[i] {
			t.Fatalf("sample %d keyframe = %v, want %v", i, s.Keyframe, wantKey[i])
		}
		if s.DTS != uint64(5000+i*100) {
			t.Fatalf("sample %d dts = %d", i, s.DTS)
		}
		if s.ByteOffset < 0 || s.ByteOffset >= int64(len(raw)) {
			t.Fatalf("sample %d offset out of range: %d", i, s.ByteOffset)
		}
	}
	// Sample offsets must be increasing by declared size and inside mdat.
	if res.Samples[1].ByteOffset-res.Samples[0].ByteOffset != 24 {
		t.Fatalf("sample stride wrong: %d", res.Samples[1].ByteOffset-res.Samples[0].ByteOffset)
	}
	if res.UnsafeBoundary != -1 || res.Truncated {
		t.Fatal("clean segment must report no unsafe boundary")
	}
	if len(res.Tracks) != 1 || res.Tracks[0].TrackID != 1 {
		t.Fatalf("tracks = %+v", res.Tracks)
	}
}

func TestParseMP4Truncated(t *testing.T) {
	raw := buildSegment(t)
	// Cut inside the mdat box so its declared length overruns the input.
	cut := len(raw) - 12
	mdatStart := int64(findTestBox(raw, "mdat"))
	partial := raw[:cut]
	res, err := ParseMP4(partial)
	if err == nil {
		t.Fatal("expected truncation error")
	}
	var te *TruncatedError
	if !AsTruncated(err, &te) {
		t.Fatalf("error is not TruncatedError: %T %v", err, err)
	}
	// The boundary must anchor at the cut box's original start offset.
	if te.Offset != mdatStart || res.UnsafeBoundary != mdatStart {
		t.Fatalf("boundary mismatch: err=%d result=%d mdat=%d",
			te.Offset, res.UnsafeBoundary, mdatStart)
	}
	if res.SafePrefix != mdatStart || !res.Truncated {
		t.Fatal("verified prefix must end at the cut box and be flagged")
	}
	rec := RecordFromTruncation("seg42.m4s", 42, err)
	if rec.Kind != fault.KindTruncatedBox || rec.ByteOffset != mdatStart {
		t.Fatalf("fault record wrong: %+v", rec)
	}
}

// findTestBox mirrors the fixture's recursive lookup without creating an
// import cycle into fixture internals.
func findTestBox(data []byte, want string) int {
	return scanTestBoxes(data, want, 0)
}

func scanTestBoxes(data []byte, want string, base int) int {
	for i := 0; i+8 <= len(data); {
		size := int(data[i])<<24 | int(data[i+1])<<16 | int(data[i+2])<<8 | int(data[i+3])
		typ := string(data[i+4 : i+8])
		if size < 8 || i+size > len(data) {
			return -1
		}
		if typ == want {
			return base + i
		}
		switch typ {
		case "moov", "trak", "mdia", "minf", "stbl", "dinf", "moof", "traf":
			if hit := scanTestBoxes(data[i+8:i+size], want, base+i+8); hit >= 0 {
				return hit
			}
		}
		i += size
	}
	return -1
}

func TestValidateWithLibraryAgrees(t *testing.T) {
	raw := buildSegment(t)
	seen, err := ValidateWithLibrary(raw)
	if err != nil {
		t.Fatalf("go-mp4 cross-check failed: %v", err)
	}
	want := map[string]bool{"styp": false, "moof": false, "tfdt": false,
		"trun": false, "mdat": false}
	for _, typ := range seen {
		if _, ok := want[typ]; ok {
			want[typ] = true
		}
	}
	for typ, found := range want {
		if !found {
			t.Fatalf("go-mp4 did not recognize %s; saw %v", typ, seen)
		}
	}
	// A truncated segment must also fail the library reader.
	_, err = ValidateWithLibrary(raw[:len(raw)-5])
	if err == nil {
		t.Fatal("library validation must reject truncated input")
	}
}

func TestOversizedRejectedBeforeAllocation(t *testing.T) {
	raw := buildSegment(t)
	big := make([]byte, 8)
	copy(big[4:8], "mdat")
	// 0x10000001 exceeds the 256 MiB box safety cap.
	big[0] = 0x10
	big[3] = 0x01
	if _, err := ParseMP4(big); err == nil {
		t.Fatal("oversized box must be rejected")
	}
	// The original valid segment still parses (no global state leakage).
	if _, err := ParseMP4(raw); err != nil {
		t.Fatal(err)
	}
}
