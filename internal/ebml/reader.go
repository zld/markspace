package ebml

import (
	"bufio"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
)

// Matroska/WebM element IDs used by the reader.
const (
	IDEBML            uint32 = 0x1A45DFA3
	IDDocType         uint32 = 0x4282
	IDDocTypeVersion  uint32 = 0x4287
	IDSegment         uint32 = 0x18538067
	IDSeekHead        uint32 = 0x114D9B74
	IDInfo            uint32 = 0x1549A966
	IDTimecodeScale   uint32 = 0x2AD7B1
	IDDuration        uint32 = 0x4489
	IDTracks          uint32 = 0x1654AE6B
	IDTrackEntry      uint32 = 0xAE
	IDTrackNumber     uint32 = 0xD7
	IDTrackUID        uint32 = 0x73C5
	IDTrackType       uint32 = 0x83
	IDCodecID         uint32 = 0x86
	IDDefaultDuration uint32 = 0x23E383
	IDVideo           uint32 = 0xE0
	IDPixelWidth      uint32 = 0xB0
	IDPixelHeight     uint32 = 0xBA
	IDAudio           uint32 = 0xE1
	IDSamplingFreq    uint32 = 0xB5
	IDChannels        uint32 = 0x9F
	IDCluster         uint32 = 0x1F43B675
	IDTimestamp       uint32 = 0xE7
	IDSimpleBlock     uint32 = 0xA3
	IDBlockGroup      uint32 = 0xA0
	IDBlock           uint32 = 0xA1
	IDBlockDuration   uint32 = 0x9B
	IDReferenceBlock  uint32 = 0xFB
	IDCues            uint32 = 0x1C53BB6B
	IDTags            uint32 = 0x1254C367
	IDChapters        uint32 = 0x1043A770
	IDAttachments     uint32 = 0x1941A469
	IDVoid            uint32 = 0xEC
)

// segmentLevel reports whether id can begin a new top-level element. It is used
// to terminate unknown-length clusters without consuming the next header.
func segmentLevel(id uint32) bool {
	switch id {
	case IDSegment, IDSeekHead, IDInfo, IDTracks, IDCluster, IDCues, IDTags, IDChapters, IDAttachments, IDEBML:
		return true
	}
	return false
}

// Header describes one EBML element and the byte span it occupies.
type Header struct {
	ID          uint32
	Offset      int64 // absolute offset of the element ID
	IDWidth     int
	SizeWidth   int
	HeaderSize  int64
	DataOffset  int64 // absolute offset of the first payload byte
	Size        uint64
	UnknownSize bool
}

// Reader is a forward-only EBML byte reader. It records the absolute offset of
// every read and hashes every byte pulled from the underlying input so the
// document digest can be tied to the exact source bytes.
type Reader struct {
	br     *bufio.Reader
	off    int64
	hasher hash.Hash
	source int64
}

// NewReader wraps r. The returned Reader buffers only a fixed window; memory
// does not grow with the input length.
func NewReader(r io.Reader) *Reader {
	h := sha256.New()
	return &Reader{
		br:     bufio.NewReaderSize(io.TeeReader(r, h), 64*1024),
		hasher: h,
	}
}

// Offset returns the absolute offset of the next unread byte.
func (r *Reader) Offset() int64 { return r.off }

// Digest returns the SHA-256 of every byte read from the source so far. Call
// Drain first to cover the complete input.
func (r *Reader) Digest() [32]byte {
	var sum [32]byte
	copy(sum[:], r.hasher.Sum(nil))
	return sum
}

// Drain consumes the remaining input so the running digest covers the file.
func (r *Reader) Drain() (int64, error) {
	n, err := io.Copy(io.Discard, r.br)
	r.off += n
	return n, err
}

func (r *Reader) readFull(p []byte) error {
	n, err := io.ReadFull(r.br, p)
	r.off += int64(n)
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	return nil
}

func (r *Reader) skip(n int64) error {
	if n < 0 {
		return errors.New("ebml: negative skip")
	}
	copied, err := io.CopyN(io.Discard, r.br, n)
	r.off += copied
	if err != nil {
		if errors.Is(err, io.EOF) {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	return nil
}

// PeekID reports the ID of the next element without consuming it. io.EOF is
// returned when the input is exhausted, with ok=false.
func (r *Reader) PeekID() (id uint32, ok bool, err error) {
	buf, peekErr := r.br.Peek(MaxIDWidth)
	if len(buf) == 0 {
		if peekErr == io.EOF {
			return 0, false, io.EOF
		}
		return 0, false, peekErr
	}
	id, _, err = ParseID(buf)
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// Next reads the next element header. The caller owns the payload and must
// consume exactly Header.Size bytes (or use SkipPayload).
func (r *Reader) Next() (Header, error) {
	start := r.off
	buf, peekErr := r.br.Peek(MaxIDWidth + MaxSizeWidth)
	if len(buf) == 0 {
		if peekErr == io.EOF {
			return Header{}, io.EOF
		}
		return Header{}, peekErr
	}
	id, idw, err := ParseID(buf)
	if err != nil {
		return Header{Offset: start}, err
	}
	size, err := ParseSize(buf[idw:])
	if err != nil {
		return Header{Offset: start, ID: id}, err
	}
	need := idw + size.Width
	if len(buf) < need {
		if peekErr != nil && !errors.Is(peekErr, io.EOF) {
			return Header{}, peekErr
		}
		return Header{Offset: start, ID: id}, io.ErrUnexpectedEOF
	}
	if err := r.skip(int64(need)); err != nil {
		return Header{Offset: start, ID: id}, err
	}
	return Header{
		ID:          id,
		Offset:      start,
		IDWidth:     idw,
		SizeWidth:   size.Width,
		HeaderSize:  int64(need),
		DataOffset:  start + int64(need),
		Size:        size.Value,
		UnknownSize: size.Unknown,
	}, nil
}

// SkipPayload consumes the declared payload of h, which must have a known size.
func (r *Reader) SkipPayload(h Header) error {
	if h.UnknownSize {
		return ErrUnknownSize
	}
	return r.skip(int64(h.Size))
}

// ReadPayload reads a known-size element payload into memory. Callers must
// bound element sizes themselves; the parser only does this for metadata.
func (r *Reader) ReadPayload(h Header, limit int) ([]byte, error) {
	if h.UnknownSize {
		return nil, ErrUnknownSize
	}
	if h.Size > uint64(limit) {
		return nil, fmt.Errorf("ebml: element 0x%x at %d exceeds limit %d", h.ID, h.Offset, limit)
	}
	buf := make([]byte, h.Size)
	if err := r.readFull(buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// ErrUnknownSize reports an operation that requires a known element size.
var ErrUnknownSize = errors.New("ebml: unknown element size")
