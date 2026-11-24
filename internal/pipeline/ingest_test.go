package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"archivecheck/internal/merr"
)

// TestUnsupportedBoxRejected proves the ingest layer refuses fMP4 boxes
// outside the fixture subset instead of silently ignoring them.
func TestUnsupportedBoxRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "odd.m4s")
	// A well-formed box of an unsupported type ("free") plus length.
	box := append([]byte{0x00, 0x00, 0x00, 0x08}, 'f', 'r', 'e', 'e')
	if err := os.WriteFile(path, box, 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := parseSegmentSamples(path)
	if err == nil {
		t.Fatal("unsupported box must be rejected")
	}
	me, ok := merr.As(err)
	if !ok || me.Kind != merr.KindContainer {
		t.Fatalf("want unsupported_media kind, got %v", err)
	}
}

// TestTruncatedCaptureIsDecodeError proves a cut-short RTP capture is a
// locatable decode error, not an end-of-stream that hides data loss.
func TestTruncatedCaptureIsDecodeError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cut.rtp")
	hdr := make([]byte, 12)
	copy(hdr[0:4], []byte{'A', 'R', 'T', 'P'})
	hdr[4] = 1
	// Claim a 200-byte datagram but append only a few bytes.
	hdr = append(hdr, 0, 0, 0, 200, 0x80, 0x60)
	if err := os.WriteFile(path, hdr, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readCapture(path); err == nil {
		t.Fatal("truncated capture must be reported, not treated as complete")
	} else if me, ok := merr.As(err); !ok || me.Kind != merr.KindDecode {
		t.Fatalf("want decode_error, got %v", err)
	}
}

// TestPathTraversalRejectedAtBoundary exercises the output guard.
func TestPathTraversalRejectedAtBoundary(t *testing.T) {
	if err := assertSafeOutput("../escape"); err == nil {
		t.Fatal("traversal output path must be rejected")
	} else if me, ok := merr.As(err); !ok || me.Kind != merr.KindArgument {
		t.Fatalf("want argument_error, got %v", err)
	}
}
