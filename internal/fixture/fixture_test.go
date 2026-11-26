package fixture

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"

	"mediatrace/internal/ebml"
)

func TestBuildWebMIsDeterministicAndScannable(t *testing.T) {
	a := BuildWebM()
	b := BuildWebM()
	if string(a) != string(b) {
		t.Fatal("fixture generation is not deterministic")
	}
	sum := sha256.Sum256(a)
	if len(hex.EncodeToString(sum[:])) != 64 {
		t.Fatal("bad digest length")
	}

	var blocks []ebml.Block
	doc, err := ebml.Scan(byteReader(a), int64(len(a)), ebml.DefaultLimits,
		func(bl ebml.Block) error { blocks = append(blocks, bl); return nil })
	if err != nil {
		t.Fatalf("scan fixture: %v", err)
	}
	if doc.DocType != "webm" || len(doc.Tracks) != 2 || doc.BlockCount != 4 {
		t.Fatalf("unexpected doc: %+v", doc)
	}
	if len(blocks) != 4 {
		t.Fatalf("want 4 blocks, got %d", len(blocks))
	}
	// Matroska block track numbers are 1-based and match TrackEntry numbers.
	wantTracks := []uint64{1, 2, 1, 2}
	for i, b := range blocks {
		if b.TrackNo != wantTracks[i] {
			t.Fatalf("block %d track=%d want %d", i, b.TrackNo, wantTracks[i])
		}
	}
	// Second cluster sits at 500 ms => 5e8 ns.
	if blocks[2].TimeNs != 500_000_000 {
		t.Fatalf("cluster2 time = %d", blocks[2].TimeNs)
	}
}

func TestBadWebMReportsStableError(t *testing.T) {
	good := BuildWebM()
	bad := BuildBadWebM(good)
	if len(bad) >= len(good) {
		t.Fatal("bad fixture must be shorter than good")
	}
	_, err := ebml.Scan(byteReader(bad), int64(len(bad)), ebml.DefaultLimits,
		func(ebml.Block) error { return nil })
	pe, ok := ebml.AsError(err)
	if !ok {
		t.Fatalf("want stable *Error, got %v", err)
	}
	if pe.Offset < 0 || pe.Offset >= int64(len(bad)) {
		t.Fatalf("error offset out of range: %d", pe.Offset)
	}
	// Repeated scans must report the identical code and offset.
	_, err2 := ebml.Scan(byteReader(bad), int64(len(bad)), ebml.DefaultLimits,
		func(ebml.Block) error { return nil })
	pe2, _ := ebml.AsError(err2)
	if pe2.Code != pe.Code || pe2.Offset != pe.Offset {
		t.Fatalf("error not stable: %+v vs %+v", pe, pe2)
	}
}

func TestEventsJSONL(t *testing.T) {
	raw, err := BuildEventsJSONL()
	if err != nil {
		t.Fatal(err)
	}
	// Four events + two ssrc maps + four rtp lines = 10 lines.
	lines := 0
	for _, b := range raw {
		if b == '\n' {
			lines++
		}
	}
	if lines != 10 {
		t.Fatalf("want 10 JSONL lines, got %d", lines)
	}
}

type bra []byte

func (b bra) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(b)) {
		return 0, io.EOF
	}
	n := copy(p, b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func byteReader(b []byte) bra { return bra(b) }
