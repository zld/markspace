package probe

import (
	"testing"
)

func TestGoMP4RecognizesGeneratedBoxes(t *testing.T) {
	f := demoFile()
	h, err := readBoxHeader(f.Data, 0, int64(len(f.Data)))
	if err != nil {
		t.Fatalf("readBoxHeader ftyp: %v", err)
	}
	if h.boxType != "ftyp" || !h.supported {
		t.Fatalf("ftyp header = %+v, want supported ftyp", h)
	}
	if h.size < 8 || h.headerSize != 8 {
		t.Fatalf("ftyp size/header = %d/%d", h.size, h.headerSize)
	}
}

func TestGoMP4RejectsTruncatedHeader(t *testing.T) {
	f := demoFile()
	// Present only four bytes where a full header is required.
	_, err := readBoxHeader(f.Data, 0, 4)
	if err == nil {
		t.Fatal("expected truncated-header fault")
	}
}

func TestSupportedByMP4Oracle(t *testing.T) {
	if !supportedByMP4("stco") || !supportedByMP4("mdat") {
		t.Fatal("known box type reported unsupported")
	}
	if supportedByMP4("zzzz") {
		t.Fatal("unknown box type reported supported")
	}
}
