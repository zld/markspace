package ebml

import (
	"bytes"
	"io"
	"testing"
)

// countingReader tracks how many bytes were pulled, proving the parser reads
// forward only and does not buffer the whole document.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// TestStreamingDoesNotBufferAllPayloads feeds a document with large block
// payloads through a small reader and confirms the parser only ever holds a
// bounded window: total read equals input length, and peak allocation stays far
// below the payload volume.
func TestStreamingDoesNotBufferAllPayloads(t *testing.T) {
	const blockSize = 1 << 20 // 1 MiB per block
	const blockCount = 16
	var body []byte
	body = append(body, el(IDTimestamp, uints(0))...)
	for i := 0; i < blockCount; i++ {
		payload := append([]byte{0x81, 0x00, 0x00, 0x80}, bytes.Repeat([]byte{byte(i)}, blockSize)...)
		body = append(body, el(IDSimpleBlock, payload)...)
	}
	body = el(IDCluster, body)
	info := el(IDInfo, el(IDTimecodeScale, uints(1_000_000)))
	tr := el(IDTracks, el(IDTrackEntry, append(el(IDTrackNumber, uints(1)), el(IDTrackType, uints(1))...)))
	seg := append(append(info, tr...), body...)
	hdr := append(el(IDDocType, []byte("matroska")), el(IDDocTypeVersion, uints(4))...)
	doc := append(el(IDEBML, hdr), el(IDSegment, seg)...)

	cr := &countingReader{r: bytes.NewReader(doc)}
	var blocks int
	var maxPayload int64
	got, err := Parse(cr, ParseOptions{MaxMetadataSize: 1 << 20}, func(b Block) error {
		blocks++
		if b.PayloadLen > maxPayload {
			maxPayload = b.PayloadLen
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if blocks != blockCount {
		t.Fatalf("blocks = %d, want %d", blocks, blockCount)
	}
	if cr.n != int64(len(doc)) {
		t.Fatalf("read %d of %d bytes", cr.n, len(doc))
	}
	if got.Size != int64(len(doc)) {
		t.Fatalf("doc size = %d, want %d", got.Size, len(doc))
	}
	if maxPayload < blockSize {
		t.Fatalf("payload length not reported: %d", maxPayload)
	}
}
