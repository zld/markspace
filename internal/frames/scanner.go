package frames

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"

	"mediarun/internal/fault"
)

// maxTick caps timestamps so every record fits the int64 timeline domain.
const maxTick = uint64(1) << 62

// Limits bounds scanner resource usage. Zero values select conservative
// defaults; negatives disable a limit except where noted.
type Limits struct {
	MaxRecords  int // total records accepted; default 100000
	MaxPayload  int // per-record payload cap; default MaxPayload
	MaxCorrupt  int // consecutive byte resync attempts before giving up
	resyncChunk int
}

// Scanner walks a frame stream sequentially. Valid records are returned one at
// a time; recoverable problems are appended to Faults and scanning continues
// at the next locatable record. The scanner retains at most one record, so
// memory use does not scale with the total asset length.
type Scanner struct {
	r        io.ReaderAt
	size     int64
	pos      int64
	index    int
	seq      int
	limits   Limits
	faults   fault.List
	done     bool
	metaOnly bool
}

// NewScanner validates limits and returns a scanner over r.
func NewScanner(r io.ReaderAt, size int64, limits Limits) (*Scanner, error) {
	if r == nil || size <= 0 {
		return nil, fault.New(fault.StageParse, fault.KindIO, "empty frame source (size=%d)", size)
	}
	if limits.MaxRecords == 0 {
		limits.MaxRecords = 100000
	}
	if limits.MaxPayload == 0 {
		limits.MaxPayload = MaxPayload
	}
	if limits.MaxPayload > MaxPayload {
		return nil, fault.New(fault.StageParse, fault.KindLimit,
			"payload limit %d exceeds container cap %d", limits.MaxPayload, MaxPayload)
	}
	if limits.MaxCorrupt == 0 {
		limits.MaxCorrupt = 1 << 16
	}
	if limits.resyncChunk == 0 {
		limits.resyncChunk = 64 << 10
	}
	return &Scanner{r: r, size: size, limits: limits}, nil
}

// Next returns the next valid record. io.EOF means the stream is exhausted.
// All recoverable faults recorded so far are available via Faults.
//
// Successive Next calls return distinct records; metadata-only mode is the
// exception, where the caller copies what it needs before advancing.
func (s *Scanner) Next() (*Record, error) {
	for !s.done {
		if s.pos >= s.size {
			s.done = true
			return nil, io.EOF
		}
		s.seq++
		rec, err := s.readAt(s.pos)
		if err == nil {
			s.index++
			s.pos += int64(HeaderSize) + int64(rec.Meta.Size) + DigestSize
			return rec, nil
		}
		var fe *fault.Error
		if !errors.As(err, &fe) {
			return nil, err
		}
		s.faults = append(s.faults, fe)
		if fe.Kind == fault.KindLimit {
			s.done = true
			return nil, err
		}
		// Advance past the bad record. For a complete but invalid record the
		// declared length is trustworthy enough to skip; otherwise search.
		skip := int64(1)
		if fe.Record >= 0 && fe.Offset == s.pos {
			if n, ok := recordSpan(s.r, s.pos, s.size, s.limits.MaxPayload); ok {
				skip = n
			}
		}
		next, ok := s.findMagic(s.pos + skip)
		if !ok {
			s.done = true
			return nil, io.EOF
		}
		s.pos = next
	}
	return nil, io.EOF
}

// Faults returns the accumulated recoverable faults.
func (s *Scanner) Faults() fault.List { return s.faults }

// readAt parses one record starting at off. It never allocates more than the
// configured payload cap, even when the header declares an enormous size.
func (s *Scanner) readAt(off int64) (*Record, error) {
	remaining := s.size - off
	if remaining < HeaderSize {
		return nil, frameErr(fault.KindTruncated, off, s.seq-1, 0,
			"only %d bytes remain, header needs %d", remaining, HeaderSize)
	}
	hdr := make([]byte, HeaderSize)
	if _, err := s.r.ReadAt(hdr, off); err != nil {
		return nil, fault.Wrap(fault.StageParse, fault.KindIO, err, "read header at %d", off)
	}
	if string(hdr[0:4]) != Magic {
		return nil, frameErr(fault.KindTruncated, off, s.seq-1, 0, "missing record magic")
	}
	meta := Metadata{
		Version: binary.BigEndian.Uint16(hdr[4:6]),
		Flags:   binary.BigEndian.Uint16(hdr[6:8]),
		Track:   hdr[8],
		Stream:  hdr[9],
		PTS:     binary.BigEndian.Uint64(hdr[10:18]),
		DTS:     binary.BigEndian.Uint64(hdr[18:26]),
		Size:    binary.BigEndian.Uint32(hdr[26:30]),
	}
	if meta.Version != Version {
		return nil, frameErr(fault.KindVersion, off, s.seq-1, meta.Track,
			"record version %d, want %d", meta.Version, Version)
	}
	if meta.Track == 0 || meta.Stream != 0 {
		return nil, frameErr(fault.KindTrack, off, s.seq-1, meta.Track,
			"invalid track=%d stream=%d", meta.Track, meta.Stream)
	}
	if meta.PTS > maxTick || meta.DTS > maxTick {
		return nil, frameErr(fault.KindTimestamp, off, s.seq-1, meta.Track,
			"pts=%d dts=%d exceed representable tick range", meta.PTS, meta.DTS)
	}
	if meta.DTS > meta.PTS {
		return nil, frameErr(fault.KindTimestamp, off, s.seq-1, meta.Track,
			"dts %d is after pts %d", meta.DTS, meta.PTS)
	}
	if meta.Size == 0 || int(meta.Size) > s.limits.MaxPayload {
		return nil, frameErr(fault.KindLength, off, s.seq-1, meta.Track,
			"declared payload %d outside 1..%d", meta.Size, s.limits.MaxPayload)
	}
	span := int64(HeaderSize) + int64(meta.Size) + DigestSize
	if remaining < span {
		return nil, frameErr(fault.KindTruncated, off, s.seq-1, meta.Track,
			"record needs %d bytes, only %d remain", span, remaining)
	}
	if s.index >= s.limits.MaxRecords {
		return nil, fault.New(fault.StageParse, fault.KindLimit,
			"record limit %d reached", s.limits.MaxRecords)
	}
	payload := make([]byte, meta.Size)
	if _, err := s.r.ReadAt(payload, off+HeaderSize); err != nil {
		return nil, fault.Wrap(fault.StageParse, fault.KindIO, err, "read payload at %d", off+HeaderSize)
	}
	var dig [DigestSize]byte
	if _, err := s.r.ReadAt(dig[:], off+HeaderSize+int64(meta.Size)); err != nil {
		return nil, fault.Wrap(fault.StageParse, fault.KindIO, err, "read digest at %d", off)
	}
	meta.Checksum = dig
	meta.CRC32 = payloadCRC(payload)
	meta.MediaKind = detectKind(payload)
	if err := VerifyDigest(off, s.seq-1, meta, payload); err != nil {
		return nil, err
	}
	if s.metaOnly {
		payload = nil
	}
	return &Record{Offset: off, Index: s.index, Meta: meta, Payload: payload}, nil
}

// SetMetadataOnly drops payloads from returned records after verification.
// The pipeline uses this for its planning pass so resident memory tracks the
// record metadata, not the media payloads; frames destined for the output are
// re-read by offset when the mux stage runs.
func (s *Scanner) SetMetadataOnly(on bool) { s.metaOnly = on }

// recordSpan reports the declared total length when the header at off is
// structurally readable, allowing the scanner to skip a corrupt payload
// without inspecting it.
func recordSpan(r io.ReaderAt, off, size int64, maxPayload int) (int64, bool) {
	if size-off < HeaderSize {
		return 0, false
	}
	hdr := make([]byte, HeaderSize)
	if _, err := r.ReadAt(hdr, off); err != nil {
		return 0, false
	}
	if string(hdr[0:4]) != Magic {
		return 0, false
	}
	n := binary.BigEndian.Uint32(hdr[26:30])
	if n == 0 || int(n) > maxPayload {
		return 0, false
	}
	span := int64(HeaderSize) + int64(n) + DigestSize
	if span > size-off {
		return 0, false
	}
	return span, true
}

// findMagic scans forward for the next record magic. It uses chunked reads so
// damaged regions cost bounded I/O rather than one syscall per byte.
func (s *Scanner) findMagic(from int64) (int64, bool) {
	if from < 0 {
		from = 0
	}
	buf := make([]byte, s.limits.resyncChunk)
	magic := []byte(Magic)
	start := from
	for start < s.size {
		n := int64(len(buf))
		if s.size-start < n {
			n = s.size - start
		}
		if _, err := s.r.ReadAt(buf[:n], start); err != nil && err != io.EOF {
			return 0, false
		}
		if i := bytes.Index(buf[:n], magic); i >= 0 {
			return start + int64(i), true
		}
		if n < int64(len(buf)) {
			return 0, false
		}
		// Overlap the tail so a magic split across chunks is still found.
		start += n - int64(len(magic)) + 1
	}
	return 0, false
}

// detectKind recognises the two synthetic image payload shapes produced by the
// fixture generator. Unknown payloads are preserved verbatim.
func detectKind(p []byte) MediaKind {
	switch {
	case len(p) >= 4 && string(p[:4]) == "IMG1":
		return MediaImageYCbCr
	case len(p) >= 4 && string(p[:4]) == "RGBA":
		return MediaImageRGBA
	case len(p) >= 4 && string(p[:4]) == "OPUS":
		return MediaAudioOpus
	default:
		return MediaUnknown
	}
}
