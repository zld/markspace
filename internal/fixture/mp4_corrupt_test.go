package fixture

import (
	"bytes"
	"testing"

	"github.com/abema/go-mp4"
)

func TestCorruptTruncation(t *testing.T) {
	clean, _ := GenerateMP4(DefaultMP4Spec(7))
	damaged := CorruptMP4(clean, CorruptTruncation)
	if len(damaged) != len(clean)-40 {
		t.Fatalf("truncated length = %d, want %d", len(damaged), len(clean)-40)
	}
	// moov must survive so the analyzer can still locate the damage.
	if findBox(damaged, "moov", 0) < 0 || findBox(damaged, "stsz", 0) < 0 {
		t.Fatal("truncation destroyed metadata boxes")
	}
}

func TestCorruptInvalidTimestamp(t *testing.T) {
	clean, _ := GenerateMP4(DefaultMP4Spec(7))
	damaged := CorruptMP4(clean, CorruptInvalidTimestamp)
	off := findBoxPayload(damaged, "stts")
	if off < 0 {
		t.Fatal("stts missing after corruption")
	}
	// First stts encountered gets delta zeroed (video track).
	if got := bytesReadU32(damaged[off+12 : off+16]); got != 0 {
		t.Fatalf("stts delta = %d, want 0", got)
	}
	// File must still be structurally parseable.
	if _, err := mp4.ReadBoxStructure(bytes.NewReader(damaged), func(h *mp4.ReadHandle) (interface{}, error) {
		if h.BoxInfo.IsSupportedType() {
			_, _ = h.Expand()
		}
		return nil, nil
	}); err != nil {
		t.Fatalf("zero-delta file must still parse: %v", err)
	}
}

func TestCorruptMissingIndex(t *testing.T) {
	clean, _ := GenerateMP4(DefaultMP4Spec(7))
	damaged := CorruptMP4(clean, CorruptMissingIndex)
	if findBox(damaged, "stss", 0) >= 0 {
		t.Fatal("stss still present after removal")
	}
	// The shortened file must remain a well-formed box tree.
	if _, err := mp4.ReadBoxStructure(bytes.NewReader(damaged), func(h *mp4.ReadHandle) (interface{}, error) {
		if h.BoxInfo.IsSupportedType() {
			_, _ = h.Expand()
		}
		return nil, nil
	}); err != nil {
		t.Fatalf("stss removal left malformed boxes: %v", err)
	}
	if findBox(damaged, "stts", 0) < 0 {
		t.Fatal("stts must survive stss removal")
	}
}

func bytesReadU32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
