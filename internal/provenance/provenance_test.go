package provenance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"archive-recovery/internal/media"
)

func TestChunkerExactByteCoverage(t *testing.T) {
	data := bytes.Repeat([]byte{0xAB}, 10000) // spans two full chunks + tail
	c, err := NewChunker(4096)
	if err != nil {
		t.Fatal(err)
	}
	first, err := c.Write(data[:4096])
	if err != nil || len(first) != 1 || first[0].Length != 4096 {
		t.Fatalf("first chunk wrong: %+v err=%v", first, err)
	}
	if _, err := c.Write(data[4096:]); err != nil {
		t.Fatal(err)
	}
	chunks := c.Close()
	if len(chunks) != 3 {
		t.Fatalf("want 3 chunks (4096,4096,1808), got %d", len(chunks))
	}
	var reassembled int64
	whole := sha256.New()
	for i, ch := range chunks {
		if ch.Index != i || ch.Offset != reassembled {
			t.Fatalf("chunk %d metadata wrong: %+v", i, ch)
		}
		reassembled += ch.Length
		part := data[ch.Offset : ch.Offset+ch.Length]
		sum := sha256.Sum256(part)
		if ch.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatalf("chunk %d hash mismatch", i)
		}
		whole.Write(part)
	}
	if reassembled != int64(len(data)) {
		t.Fatalf("coverage %d != input %d", reassembled, len(data))
	}
	if hex.EncodeToString(whole.Sum(nil)) != sha256Hex(data) {
		t.Fatal("per-chunk hashes do not reconstruct the whole-file hash")
	}
}

func TestChunkerRejectsBadSize(t *testing.T) {
	if _, err := NewChunker(0); media.ClassOf(err) != media.ClassInvalidArgument {
		t.Fatalf("want invalid argument, got %v", err)
	}
}

func TestHashReaderMatchesWholeFile(t *testing.T) {
	data := bytes.Repeat([]byte{1, 2, 3, 4, 5}, 2000)
	chunks, whole, err := HashReader(bytes.NewReader(data), 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 20 {
		t.Fatalf("want 20 chunks, got %d", len(chunks))
	}
	if hex.EncodeToString(whole) != sha256Hex(data) {
		t.Fatal("streaming whole-file digest mismatch")
	}
}

func TestDeferredConfirmationAndManifestDigest(t *testing.T) {
	data := bytes.Repeat([]byte{7}, 4096*3)
	chunks, whole, err := HashReader(bytes.NewReader(data), 4096)
	if err != nil {
		t.Fatal(err)
	}
	b := NewBuilder("av1", "av1-1", hex.EncodeToString(whole), int64(len(data)), 4096)
	b.AddChunks(chunks)

	// Complete unit straddling the chunk 0/1 boundary confirms both.
	b.AddFrame(FrameRecord{FrameSeq: 0, Offset: 4000, Length: 200, IsKeyframe: true,
		RawPTS: 0, HasRawPTS: true, RepairedPTS: 0, RepairReason: "ok"})
	// Truncated unit inside chunk 2: recorded as error, never confirmed.
	b.AddError(media.UnitError{Offset: 8200, Length: 50, Class: media.ClassRecoverable, Reason: "truncated OBU"})

	m := b.Finalize()
	if len(m.ConfirmedChunks) != 2 || m.ConfirmedChunks[0] != 0 || m.ConfirmedChunks[1] != 1 {
		t.Fatalf("only chunks 0 and 1 may be confirmed, got %v", m.ConfirmedChunks)
	}
	if len(m.Errors) != 1 || m.Errors[0].Class != "recoverable" {
		t.Fatalf("error record wrong: %+v", m.Errors)
	}

	d1, err := m.Digest()
	if err != nil {
		t.Fatal(err)
	}
	d2, err := m.Digest()
	if err != nil || d1 != d2 || len(d1) != 64 {
		t.Fatalf("manifest digest must be stable and 64 hex chars: %q %q", d1, d2)
	}

	// Any changed byte in a frame record must change the digest.
	m.Frames[0].RepairReason = "regressive_timestamp"
	d3, _ := m.Digest()
	if d3 == d1 {
		t.Fatal("manifest digest failed to detect content change")
	}
}

func TestManifestJSONIsDeterministic(t *testing.T) {
	b := NewBuilder("h264", "h264-1", "abcd", 100, 10)
	b.AddChunks([]Chunk{{Index: 0, Offset: 0, Length: 10, SHA256: "aa"}})
	b.AddFrame(FrameRecord{FrameSeq: 0, Offset: 0, Length: 10, IsKeyframe: true})
	m := b.Finalize()
	raw1, _ := m.Marshal()
	raw2, _ := m.Marshal()
	if !bytes.Equal(raw1, raw2) {
		t.Fatal("canonical serialization must be byte-identical")
	}
	if !bytes.Contains(raw1, []byte(`"adapter_version": "h264-1"`)) {
		t.Fatalf("adapter version missing from manifest: %s", raw1)
	}
}

func sha256Hex(p []byte) string {
	sum := sha256.Sum256(p)
	return hex.EncodeToString(sum[:])
}
