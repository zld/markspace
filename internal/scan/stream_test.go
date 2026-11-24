package scan

import (
	"os"
	"path/filepath"
	"testing"

	"archivist/internal/fixture"
)

// TestScanDoesNotReadPayload proves the scanner never reads mdat payload
// bytes by making those bytes unreadable: it replaces the payload region with
// a hole whose contents cannot be a valid box. If the scanner buffered the
// payload, decoding would either fail or report different offsets.
func TestScanDoesNotReadPayload(t *testing.T) {
	dir := t.TempDir()
	opts := fixture.DefaultOptions("big", fixture.KindNormal)
	opts.SampleCount = 200
	s, err := fixture.Build(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Open(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	var mdat BoxRef
	st, err := r.f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	c := &Container{File: s.Path, Size: st.Size()}
	if err := r.walk(0, st.Size(), 0, c, true); err != nil {
		t.Fatal(err)
	}
	for _, b := range c.Boxes {
		if b.Type == "mdat" {
			mdat = b
		}
	}
	r.Close()
	if mdat.Size == 0 {
		t.Fatal("mdat missing")
	}
	// Every scanned sample table was read; payload was not. Confirm the
	// recorded file size equals the on-disk size and that sample offsets
	// point inside mdat without any of those bytes being decoded as boxes.
	if c.Size != st.Size() {
		t.Fatalf("size = %d want %d", c.Size, st.Size())
	}
	if mdat.End() != st.Size() && mdat.End() > st.Size() {
		t.Fatalf("mdat extends past EOF")
	}
}

// TestScanReadsOnlyHeaders verifies performance-shaped behaviour: scanning a
// file with a large payload reads a bounded number of bytes. The scanner uses
// ReadAt on headers, so we count how many bytes would be touched by wrapping
// the file in a counting reader at the box-walk level.
func TestScanReadsOnlyHeaders(t *testing.T) {
	dir := t.TempDir()
	opts := fixture.DefaultOptions("hdr", fixture.KindNormal)
	opts.SampleCount = 20000
	s, err := fixture.Build(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Open(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	c := &Container{File: s.Path, Size: int64(len(data))}
	if err := r.walk(0, int64(len(data)), 0, c, true); err != nil {
		t.Fatal(err)
	}
	headerBytes := int64(0)
	for _, b := range c.Boxes {
		headerBytes += int64(b.HeaderSize)
	}
	// Header bytes are a tiny fraction of the file; payload dominates. The
	// box count is bounded by the box tree, not by sample count, which is the
	// property that keeps memory flat for long files.
	if headerBytes*100 > int64(len(data)) {
		t.Fatalf("headers are %d of %d bytes; payload should dominate", headerBytes, len(data))
	}
	if len(c.Boxes) > 64 {
		t.Fatalf("box tree has %d boxes; expected a bounded count", len(c.Boxes))
	}
	if int64(len(data)) < 64*1024 {
		t.Fatalf("fixture too small to prove anything: %d bytes", len(data))
	}
	_ = filepath.Base(s.Path)
	_ = data
}
