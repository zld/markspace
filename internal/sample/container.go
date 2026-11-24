package sample

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	FileMagic  = "MDPIPE01"
	ChunkMagic = "CHNK"
	ChunkRTP   = byte(1)
	ChunkRTCP  = byte(2)
)

// Chunk is one length-delimited RTP or RTCP datagram in the generated source.
type Chunk struct {
	Kind   byte
	Flags  byte
	Data   []byte
	Offset int64
}

// WriteContainer creates a simple length-prefixed local source format. Packet
// boundaries remain explicit even after random loss, reordering and corruption.
func WriteContainer(chunks []Chunk) []byte {
	out := make([]byte, 0, 8+len(chunks)*192)
	out = append(out, FileMagic...)
	for i := range chunks {
		chunks[i].Offset = int64(len(out))
		out = append(out, ChunkMagic...)
		out = append(out, chunks[i].Kind, chunks[i].Flags)
		var lenBuf [2]byte
		binary.BigEndian.PutUint16(lenBuf[:], uint16(len(chunks[i].Data)))
		out = append(out, lenBuf[:]...)
		out = append(out, chunks[i].Data...)
	}
	return out
}

// Reader streams chunks from a seekable source so memory stays bounded by the
// queue budget rather than the complete input size.
type Reader struct {
	r      io.ReadSeeker
	offset int64
}

func NewReader(r io.ReadSeeker) (*Reader, error) {
	magic := make([]byte, len(FileMagic))
	if _, err := io.ReadFull(r, magic); err != nil {
		return nil, fmt.Errorf("read source magic: %w", err)
	}
	if string(magic) != FileMagic {
		return nil, fmt.Errorf("bad source magic %q", string(magic))
	}
	return &Reader{r: r, offset: int64(len(FileMagic))}, nil
}

func (r *Reader) Offset() int64 { return r.offset }

func (r *Reader) SeekChunk(offset int64) error {
	n, err := r.r.Seek(offset, io.SeekStart)
	if err != nil {
		return err
	}
	r.offset = n
	return nil
}

func (r *Reader) ReadChunk() (Chunk, error) {
	offset := r.offset
	header := make([]byte, 8)
	if _, err := io.ReadFull(r.r, header); err != nil {
		return Chunk{Offset: offset}, err
	}
	if string(header[0:4]) != ChunkMagic {
		return Chunk{Offset: offset}, fmt.Errorf("bad chunk magic at offset %d", offset)
	}
	n := binary.BigEndian.Uint16(header[6:8])
	data := make([]byte, n)
	if _, err := io.ReadFull(r.r, data); err != nil {
		return Chunk{Offset: offset, Kind: header[4]}, err
	}
	r.offset = offset + int64(8+n)
	return Chunk{Kind: header[4], Flags: header[5], Data: data, Offset: offset}, nil
}
