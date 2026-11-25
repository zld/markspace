package index

import (
	"strings"
	"testing"

	"mediad/internal/stream"
)

func TestParseAndValidateH264(t *testing.T) {
	text := `
# keyframe index for fixture stream
0 100 IDR
5 250 IDR
10 90 IDR
15 400 bad-type
20 500 IDR
25 -1 IDR
30 x700 IDR
`
	raws, err := Parse(strings.NewReader(text))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	idx, err := Validate(stream.H264, raws, 600)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	// Valid: 100, 250, 500. Invalid: non-monotonic 90, bad type, -1, bad number.
	if idx.ValidCount != 3 || idx.InvalidCount != 4 {
		t.Fatalf("counts = %d/%d, want 3/4: %+v", idx.ValidCount, idx.InvalidCount, idx.Entries)
	}
	wantReason := map[uint64]string{
		10: "strictly greater", // non-monotonic offset
		15: "keyframe type",    // wrong type
		25: "stream range",     // negative offset out of range
		30: "numeric",          // malformed number field
	}
	for _, e := range idx.Entries {
		if e.Valid {
			continue
		}
		matched := false
		for _, frag := range wantReason {
			if strings.Contains(e.Reason, frag) {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("unexpected reason %q for %+v", e.Reason, e)
		}
	}
}

func TestValidateMonotonicUsesLastValid(t *testing.T) {
	text := `0 10 IDR
1 10 IDR
2 11 IDR
3 9 IDR
4 12 IDR`
	raws, _ := Parse(strings.NewReader(text))
	idx, err := Validate(stream.H264, raws, 100)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	// Frames 1 (dup), 3 (below last valid 11) invalid; frame 4 compares
	// against last *valid* offset 11, so it stays valid.
	want := map[uint64]bool{0: true, 1: false, 2: true, 3: false, 4: true}
	for _, e := range idx.Entries {
		if e.Valid != want[e.Raw.Frame] {
			t.Fatalf("frame %d valid=%v reason=%q", e.Raw.Frame, e.Valid, e.Reason)
		}
	}
	if got := idx.ValidOffsets(); len(got) != 3 || got[2] != 12 {
		t.Fatalf("valid offsets = %v", got)
	}
}

func TestValidateUnknownCodecFails(t *testing.T) {
	if _, err := Validate(stream.Codec("vp8"), nil, 10); err == nil {
		t.Fatal("unknown codec must fail")
	}
}
