// Package provenance records which input bytes produced every result:
// fixed-size chunk hashes, unit boundaries, repair decisions and parser
// identity, all serialized into an auditable manifest.
package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"io"

	"archive-recovery/internal/media"
)

// ErrBadChunkSize is raised when a non-positive chunk size is requested.
var ErrBadChunkSize = media.NewError(media.ClassInvalidArgument, "provenance",
	"chunk-bytes must be positive")

// Chunk is one fixed-size input range with its SHA-256. The final chunk may
// be shorter.
type Chunk struct {
	Index  int    `json:"index"`
	Offset int64  `json:"offset"`
	Length int64  `json:"length"`
	SHA256 string `json:"sha256"`
}

// Chunker hashes the input stream in fixed-size pieces. Unit boundary
// information is attached out-of-band; when a boundary falls inside a
// chunk, the chunk is marked pending and only confirmed once every unit it
// touches has been parsed, guaranteeing that hashes correspond exactly to
// verified input bytes.
type Chunker struct {
	chunkBytes int64

	pending []byte
	offset  int64 // absolute offset of the pending window start
	chunks  []Chunk
}

// NewChunker requires a positive chunk size.
func NewChunker(chunkBytes int64) (*Chunker, error) {
	if chunkBytes <= 0 {
		return nil, ErrBadChunkSize
	}
	return &Chunker{chunkBytes: chunkBytes}, nil
}

// Write appends input bytes; complete chunks are hashed immediately. The
// returned chunks are also retained for the final manifest.
func (c *Chunker) Write(p []byte) ([]Chunk, error) {
	var emitted []Chunk
	c.pending = append(c.pending, p...)
	for int64(len(c.pending)) >= c.chunkBytes {
		part := c.pending[:c.chunkBytes]
		c.pending = c.pending[c.chunkBytes:]
		ch := c.hashOne(part)
		c.chunks = append(c.chunks, ch)
		emitted = append(emitted, ch)
	}
	return emitted, nil
}

// Close flushes the final partial chunk (if any) and returns all chunks.
func (c *Chunker) Close() []Chunk {
	if len(c.pending) > 0 {
		ch := c.hashOne(c.pending)
		c.pending = nil
		c.chunks = append(c.chunks, ch)
	}
	return c.chunks
}

func (c *Chunker) hashOne(part []byte) Chunk {
	h := sha256.Sum256(part)
	ch := Chunk{
		Index: len(c.chunks), Offset: c.offset,
		Length: int64(len(part)), SHA256: hex.EncodeToString(h[:]),
	}
	c.offset += int64(len(part))
	return ch
}

// HashReader streams r through the chunker with bounded memory.
func HashReader(r io.Reader, chunkBytes int64) ([]Chunk, []byte, error) {
	c, err := NewChunker(chunkBytes)
	if err != nil {
		return nil, nil, err
	}
	buf := make([]byte, chunkBytes)
	total := sha256.New()
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			if _, err := c.Write(buf[:n]); err != nil {
				return nil, nil, err
			}
			total.Write(buf[:n])
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, nil, rerr
		}
	}
	return c.Close(), total.Sum(nil), nil
}
