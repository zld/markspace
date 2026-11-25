package wav

import (
	"encoding/binary"
	"testing"
)

func makeChunk(id string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	copy(b[0:4], id)
	binary.LittleEndian.PutUint32(b[4:8], uint32(len(payload)))
	copy(b[8:], payload)
	if len(payload)&1 == 1 {
		b = append(b, 0) // even padding
	}
	return b
}

func TestWalkChunksBoundariesAndPadding(t *testing.T) {
	b := append(makeChunk("fmt ", make([]byte, 16)), makeChunk("xtr1", []byte{1, 2, 3})...) // odd payload
	b = append(b, makeChunk("data", make([]byte, 8))...)
	chunks, findings := WalkChunks(b, 0)
	if len(findings) != 0 {
		t.Fatalf("unexpected findings: %v", findings)
	}
	if len(chunks) != 3 {
		t.Fatalf("got %d chunks: %+v", len(chunks), chunks)
	}
	if chunks[1].Size != 3 || chunks[1].HeaderOffset != 24 || chunks[1].DataOffset != 32 {
		t.Fatalf("xtr1 boundaries wrong: %+v", chunks[1])
	}
	// Odd chunk padded: xtr1 ends at 35, pad -> 36, data header at 36.
	if chunks[2].HeaderOffset != 36 {
		t.Fatalf("data offset = %d, want 36", chunks[2].HeaderOffset)
	}
}

func TestWalkChunksTruncatedHeaderReportsOffset(t *testing.T) {
	// Complete first chunk then 5 dangling header bytes.
	good := makeChunk("data", []byte{1, 2, 3, 4})
	good = append(good, []byte("ABCDE")...)
	chunks, findings := WalkChunks(good, 0)
	if len(chunks) != 1 {
		t.Fatalf("want one chunk, got %d", len(chunks))
	}
	if len(findings) != 1 || findings[0].Code != 1013 {
		t.Fatalf("want E1013 truncated header, got %v", findings)
	}
	if findings[0].Offset != int64(len(good)-5) {
		t.Fatalf("offset = %d, want %d", findings[0].Offset, len(good)-5)
	}
}

func TestWalkChunksOutOfBounds(t *testing.T) {
	b := makeChunk("data", make([]byte, 16))
	binary.LittleEndian.PutUint32(b[4:8], 4096) // lie about size
	chunks, findings := WalkChunks(b, 0)
	if len(findings) != 1 || findings[0].Code != 1012 {
		t.Fatalf("want E1012, got %v", findings)
	}
	if !chunks[0].OutOfBounds || chunks[0].Available != 16 {
		t.Fatalf("chunk clamp wrong: %+v", chunks[0])
	}
	if findings[0].Offset != 0 {
		t.Fatalf("offset = %d, want 0", findings[0].Offset)
	}
}
