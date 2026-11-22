package media

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// Frame record wire format (all integers big-endian):
//
//	offset  size  field
//	0       4     magic "MFR1"
//	4       1     format version (1)
//	5       1     flags (bit 0: metadata present)
//	6       2     header size (96)
//	8       4     sequence number
//	12      4     RTP timestamp (32-bit, wraps)
//	16      8     capture time in Unix milliseconds
//	24      2     width
//	26      2     height
//	28      4     metadata length
//	32      4     payload length
//	36      4     nominal duration in timescale ticks
//	40      4     stream id
//	44      4     reserved (zero)
//	48      32    SHA-256 over bytes [0,48) || payload || metadata
//	80      16    reserved (zero)
//
// The payload (width*height bytes) and the JSON metadata follow the header.
const (
	FrameMagic      = "MFR1"
	FrameVersion    = 1
	FrameHeaderSize = 96
	frameHashOffset = 48
	frameHashEnd    = 80
	flagMetaPresent = 1 << 0
)

// Default hard limits protect the scanner from hostile or corrupt input.
const (
	DefaultMaxPayload   = 64 << 20
	DefaultMaxMetaBytes = 1 << 20
	DefaultMaxDimension = 16384
)

// Limits bounds a frame record before allocation.
type Limits struct {
	MaxPayload   uint32
	MaxMetaBytes uint32
	MaxDimension uint32
}

// DefaultLimits returns the standard record limits.
func DefaultLimits() Limits {
	return Limits{
		MaxPayload:   DefaultMaxPayload,
		MaxMetaBytes: DefaultMaxMetaBytes,
		MaxDimension: DefaultMaxDimension,
	}
}

// MetaStatus describes how much trust to place in a frame's metadata.
type MetaStatus string

const (
	MetaPresent MetaStatus = "present"
	MetaAbsent  MetaStatus = "absent"
	MetaCorrupt MetaStatus = "corrupt"
)

// Meta is the optional JSON metadata block attached to a frame. VolumeDBFS is
// a compact loudness summary; a nil pointer means the value was not captured
// and must be treated as unknown rather than silent.
type Meta struct {
	VolumeDBFS *float64          `json:"volume_dbfs,omitempty"`
	Labels     []string          `json:"labels,omitempty"`
	Extra      map[string]string `json:"extra,omitempty"`
}

// Frame is one decoded frame record. Offset is the record's byte offset in the
// source stream and is used to locate damage.
type Frame struct {
	Offset        int64
	Seq           uint32
	RTPTimestamp  uint32
	CaptureTimeMS int64
	Width         uint16
	Height        uint16
	DurationTicks uint32
	StreamID      uint32
	Payload       []byte
	MetaRaw       []byte
	Meta          Meta
	MetaStatus    MetaStatus
	Digest        [32]byte
	// Ticks is the monotonic media time assigned by the scanner's clock.
	Ticks int64
}

// Len returns the total encoded record size for this frame.
func (f *Frame) Len() int {
	return FrameHeaderSize + len(f.Payload) + len(f.MetaRaw)
}

// PixelBytes is the expected payload size for the frame dimensions.
func (f *Frame) PixelBytes() int { return int(f.Width) * int(f.Height) }

// recordDigest computes SHA-256 over the header prefix, payload and metadata.
func recordDigest(prefix []byte, payload, meta []byte) [32]byte {
	h := sha256.New()
	h.Write(prefix)
	h.Write(payload)
	h.Write(meta)
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

// Encode serializes the frame, recomputing the digest and metadata length.
func (f *Frame) Encode() ([]byte, error) {
	if len(f.Payload) > DefaultMaxPayload || len(f.MetaRaw) > DefaultMaxMetaBytes {
		return nil, New(CatSizeLimit, StageScan, "frame payload=%d meta=%d exceeds limits",
			len(f.Payload), len(f.MetaRaw)).WithOffset(f.Offset).WithSeq(f.Seq)
	}
	buf := make([]byte, FrameHeaderSize+len(f.Payload)+len(f.MetaRaw))
	h := buf[:FrameHeaderSize]
	copy(h[0:4], FrameMagic)
	h[4] = FrameVersion
	if len(f.MetaRaw) > 0 {
		h[5] = flagMetaPresent
	}
	binary.BigEndian.PutUint16(h[6:8], FrameHeaderSize)
	binary.BigEndian.PutUint32(h[8:12], f.Seq)
	binary.BigEndian.PutUint32(h[12:16], f.RTPTimestamp)
	binary.BigEndian.PutUint64(h[16:24], uint64(f.CaptureTimeMS))
	binary.BigEndian.PutUint16(h[24:26], f.Width)
	binary.BigEndian.PutUint16(h[26:28], f.Height)
	binary.BigEndian.PutUint32(h[28:32], uint32(len(f.MetaRaw)))
	binary.BigEndian.PutUint32(h[32:36], uint32(len(f.Payload)))
	binary.BigEndian.PutUint32(h[36:40], f.DurationTicks)
	binary.BigEndian.PutUint32(h[40:44], f.StreamID)
	sum := recordDigest(h[:frameHashOffset], f.Payload, f.MetaRaw)
	copy(h[frameHashOffset:frameHashEnd], sum[:])
	f.Digest = sum
	copy(buf[FrameHeaderSize:], f.Payload)
	copy(buf[FrameHeaderSize+len(f.Payload):], f.MetaRaw)
	return buf, nil
}

// NewFrameReader wraps r for sequential frame record decoding.
func NewFrameReader(r io.Reader, limits Limits) *FrameReader {
	if limits.MaxPayload == 0 {
		limits = DefaultLimits()
	}
	return &FrameReader{
		r:      bufio.NewReaderSize(r, 64<<10),
		limits: limits,
	}
}

// FrameReader decodes a stream of frame records and tracks byte offsets.
type FrameReader struct {
	r      *bufio.Reader
	limits Limits
	offset int64
	err    error
}

// Offset returns the byte offset of the next record to be read.
func (fr *FrameReader) Offset() int64 { return fr.offset }

// Err returns the sticky error, if any.
func (fr *FrameReader) Err() error { return fr.err }

// Drain consumes and discards any unread bytes, updating the offset. It is
// used after a scan stops early so an input digest still covers the whole
// stream.
func (fr *FrameReader) Drain() (int64, error) {
	n, err := io.Copy(io.Discard, fr.r)
	fr.offset += n
	if err != nil {
		return n, err
	}
	return n, nil
}

func (fr *FrameReader) readFull(buf []byte) error {
	n, err := io.ReadFull(fr.r, buf)
	fr.offset += int64(n)
	if err == io.EOF {
		return io.EOF
	}
	if err == io.ErrUnexpectedEOF {
		return New(CatTruncatedFrame, StageScan, "record truncated after %d header bytes", n).
			WithOffset(fr.offset - int64(n))
	}
	return err
}

// Next decodes one frame. It returns io.EOF at a clean end of stream. Any
// malformed, truncated, oversized or checksum-failing record produces a
// *media.Error carrying the byte offset; the reader then becomes stuck on the
// same error so callers cannot silently skip damage.
func (fr *FrameReader) Next() (*Frame, error) {
	if fr.err != nil {
		return nil, fr.err
	}
	start := fr.offset
	hdr := make([]byte, FrameHeaderSize)
	if err := fr.readFull(hdr); err != nil {
		if err == io.EOF && fr.offset == start {
			return nil, io.EOF
		}
		fr.err = err
		return nil, err
	}
	if string(hdr[0:4]) != FrameMagic {
		fr.err = New(CatBadMagic, StageScan, "unexpected magic %q", string(hdr[0:4])).WithOffset(start)
		return nil, fr.err
	}
	if hdr[4] != FrameVersion {
		fr.err = New(CatBadMagic, StageScan, "unsupported frame version %d", hdr[4]).WithOffset(start)
		return nil, fr.err
	}
	hdrLen := binary.BigEndian.Uint16(hdr[6:8])
	if int(hdrLen) != FrameHeaderSize {
		fr.err = New(CatBadMagic, StageScan, "unexpected header size %d", hdrLen).WithOffset(start)
		return nil, fr.err
	}
	f := &Frame{
		Offset:        start,
		Seq:           binary.BigEndian.Uint32(hdr[8:12]),
		RTPTimestamp:  binary.BigEndian.Uint32(hdr[12:16]),
		CaptureTimeMS: int64(binary.BigEndian.Uint64(hdr[16:24])),
		Width:         binary.BigEndian.Uint16(hdr[24:26]),
		Height:        binary.BigEndian.Uint16(hdr[26:28]),
		DurationTicks: binary.BigEndian.Uint32(hdr[36:40]),
		StreamID:      binary.BigEndian.Uint32(hdr[40:44]),
	}
	metaLen := binary.BigEndian.Uint32(hdr[28:32])
	payloadLen := binary.BigEndian.Uint32(hdr[32:36])
	copy(f.Digest[:], hdr[frameHashOffset:frameHashEnd])
	if err := fr.validateShape(f, payloadLen, metaLen); err != nil {
		fr.err = err.WithOffset(start).WithSeq(f.Seq)
		return nil, fr.err
	}
	if payloadLen > 0 {
		f.Payload = make([]byte, payloadLen)
		if err := fr.readFull(f.Payload); err != nil {
			err = New(CatTruncatedFrame, StageScan, "payload truncated: %v", err).
				WithOffset(start).WithSeq(f.Seq)
			fr.err = err
			return nil, err
		}
	}
	if metaLen > 0 {
		f.MetaRaw = make([]byte, metaLen)
		if err := fr.readFull(f.MetaRaw); err != nil {
			err = New(CatTruncatedFrame, StageScan, "metadata truncated: %v", err).
				WithOffset(start).WithSeq(f.Seq)
			fr.err = err
			return nil, err
		}
	}
	if got := recordDigest(hdr[:frameHashOffset], f.Payload, f.MetaRaw); got != f.Digest {
		fr.err = New(CatChecksum, StageScan, "SHA-256 mismatch (header=%x computed=%x)",
			f.Digest[:8], got[:8]).WithOffset(start).WithSeq(f.Seq)
		return nil, fr.err
	}
	f.MetaStatus = parseMeta(f)
	return f, nil
}

func (fr *FrameReader) validateShape(f *Frame, payloadLen, metaLen uint32) *Error {
	if uint32(f.Width) > fr.limits.MaxDimension || uint32(f.Height) > fr.limits.MaxDimension {
		return New(CatSizeLimit, StageScan, "dimensions %dx%d exceed limit %d",
			f.Width, f.Height, fr.limits.MaxDimension)
	}
	if payloadLen > fr.limits.MaxPayload {
		return New(CatSizeLimit, StageScan, "payload %d exceeds limit %d", payloadLen, fr.limits.MaxPayload)
	}
	if metaLen > fr.limits.MaxMetaBytes {
		return New(CatSizeLimit, StageScan, "metadata %d exceeds limit %d", metaLen, fr.limits.MaxMetaBytes)
	}
	want := uint32(f.Width) * uint32(f.Height)
	if payloadLen != want {
		return New(CatSizeLimit, StageScan, "payload %d != width*height %d", payloadLen, want)
	}
	return nil
}

func parseMeta(f *Frame) MetaStatus {
	if len(f.MetaRaw) == 0 {
		f.Meta = Meta{}
		return MetaAbsent
	}
	if err := json.Unmarshal(f.MetaRaw, &f.Meta); err != nil {
		f.Meta = Meta{}
		return MetaCorrupt
	}
	if f.Meta.VolumeDBFS != nil {
		v := *f.Meta.VolumeDBFS
		if v > 0 || v < -200 {
			f.Meta.VolumeDBFS = nil
			return MetaCorrupt
		}
	}
	return MetaPresent
}

// ReadAll decodes every record from r. It stops at the first error, returning
// the frames decoded so far together with that error.
func ReadAll(r io.Reader, limits Limits) ([]*Frame, error) {
	fr := NewFrameReader(r, limits)
	var frames []*Frame
	for {
		f, err := fr.Next()
		if err == io.EOF {
			return frames, nil
		}
		if err != nil {
			return frames, err
		}
		frames = append(frames, f)
	}
}

// DescribeError renders a deterministic single-line location string for logs.
func DescribeError(err error) string {
	if e, ok := AsError(err); ok {
		return fmt.Sprintf("%v (recoverable=%t; %s)", e, e.Recoverable, e.Suggestion)
	}
	return err.Error()
}

// EncodeStream serializes frames back-to-back, recomputing each digest. The
// returned bytes are a complete, valid frame stream for the given frames.
func EncodeStream(frames []*Frame) ([]byte, error) {
	var out []byte
	for _, f := range frames {
		b, err := f.Encode()
		if err != nil {
			return nil, err
		}
		out = append(out, b...)
	}
	return out, nil
}

// ParseFrameAt decodes and fully validates the record beginning at offset in
// buf. It is used by streaming stages that consume verified index entries
// without trusting the index alone.
func ParseFrameAt(buf []byte, offset int64, limits Limits) (*Frame, error) {
	if offset < 0 || offset >= int64(len(buf)) {
		return nil, New(CatTruncatedFrame, StageScan, "offset %d outside input of %d bytes",
			offset, len(buf)).WithOffset(offset)
	}
	if limits.MaxPayload == 0 {
		limits = DefaultLimits()
	}
	fr := NewFrameReader(bytesReaderAt(buf, offset), limits)
	f, err := fr.Next()
	if err != nil {
		if e, ok := AsError(err); ok {
			e.FrameOffset = offset + e.FrameOffset
			return nil, e
		}
		return nil, err
	}
	f.Offset = offset
	return f, nil
}

type sliceReader struct {
	buf []byte
	pos int
}

func bytesReaderAt(buf []byte, off int64) *sliceReader {
	return &sliceReader{buf: buf, pos: int(off)}
}

func (s *sliceReader) Read(p []byte) (int, error) {
	if s.pos >= len(s.buf) {
		return 0, io.EOF
	}
	n := copy(p, s.buf[s.pos:])
	s.pos += n
	return n, nil
}
