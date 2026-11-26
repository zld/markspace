package ebml

import (
	"testing"
)

func TestCrossValidateAcceptsWellFormedBlock(t *testing.T) {
	// Track 1, relative timecode 0, keyframe, two frame bytes.
	raw := []byte{0x81, 0x00, 0x00, 0x80, 0xAA, 0xBB}
	parsed := Block{
		TrackNo:    1,
		TimeNs:     0,
		ElemOffset: 42,
	}
	if err := CrossValidateBlock(raw, parsed, 0, 1_000_000); err != nil {
		t.Fatalf("well-formed block rejected: %v", err)
	}
}

func TestCrossValidateDetectsTimecodeMismatch(t *testing.T) {
	// Reference decoder sees relative timecode 0; scanner claims 1 ms.
	raw := []byte{0x81, 0x00, 0x00, 0x80, 0xAA}
	parsed := Block{
		TrackNo:    1,
		TimeNs:     1_000_000, // implies rel=1 with scale 1e6 and clusterTC 0
		ElemOffset: 7,
	}
	err := CrossValidateBlock(raw, parsed, 0, 1_000_000)
	pe, ok := AsError(err)
	if !ok || pe.Code != ErrInvalidEBML || pe.Offset != 7 {
		t.Fatalf("want invalid-container error at offset 7, got %v", err)
	}
}
