package media

import (
	"encoding/binary"
	"testing"
)

func TestFrameRejectsOversizedDimensions(t *testing.T) {
	f := &Frame{Width: 4, Height: 4, Payload: make([]byte, 16)}
	b, err := f.Encode()
	if err != nil {
		t.Fatal(err)
	}
	// Claim a huge width; the dimension limit is checked before allocation.
	binary.BigEndian.PutUint16(b[24:26], 40000)
	_, err = ParseFrameAt(b, 0, DefaultLimits())
	if err == nil {
		t.Fatal("expected a size-limit error")
	}
	e, ok := AsError(err)
	if !ok || e.Category != CatSizeLimit || e.FrameOffset != 0 {
		t.Fatalf("error=%v", err)
	}
}

func TestFrameRejectsPayloadDimensionMismatch(t *testing.T) {
	f := &Frame{Width: 4, Height: 4, Payload: make([]byte, 16)}
	b, err := f.Encode()
	if err != nil {
		t.Fatal(err)
	}
	// Claim 8x8 (payload should be 64) without changing the 16-byte payload.
	binary.BigEndian.PutUint16(b[24:26], 8)
	binary.BigEndian.PutUint16(b[26:28], 8)
	_, err = ParseFrameAt(b, 0, DefaultLimits())
	if err == nil {
		t.Fatal("expected a payload/dimension mismatch error")
	}
	if CategoryOf(err) != CatSizeLimit {
		t.Fatalf("category=%s", CategoryOf(err))
	}
}

func TestFrameRejectsOversizedMetadata(t *testing.T) {
	f := &Frame{Width: 4, Height: 4, Payload: make([]byte, 16), MetaRaw: []byte(`{"a":"b"}`)}
	b, err := f.Encode()
	if err != nil {
		t.Fatal(err)
	}
	lim := DefaultLimits()
	lim.MaxMetaBytes = 4
	_, err = ParseFrameAt(b, 0, lim)
	if err == nil {
		t.Fatal("expected a metadata size-limit error")
	}
	if CategoryOf(err) != CatSizeLimit {
		t.Fatalf("category=%s", CategoryOf(err))
	}
}
