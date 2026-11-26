// Package bitstream parses elementary-stream boundaries and keyframes.
//
// The parsers are streaming: callers supply an io.Reader and NAL/OBU records
// are produced without requiring the whole media file to be held in memory.
package bitstream

import (
	"bufio"
	"io"

	"github.com/zld/medcheck/pkg/media"
)

// Codec identifies an elementary stream syntax.
type Codec string

const (
	CodecH264 Codec = "h264"
	CodecH265 Codec = "h265"
	CodecAV1  Codec = "av1"
)

// Unit is one NAL unit or OBU. Offsets are absolute in the scanned stream.
type Unit struct {
	Index         int    `json:"index"`
	Codec         Codec  `json:"codec"`
	Kind          string `json:"kind"`
	Offset        int64  `json:"offset"`
	HeaderOffset  int64  `json:"header_offset"`
	PayloadOffset int64  `json:"payload_offset"`
	Length        int64  `json:"length"`
	End           int64  `json:"end"`
	Keyframe      bool   `json:"keyframe"`
	Complete      bool   `json:"complete"`
}

// Result contains all usable units plus non-fatal corruption records.
type Result struct {
	Codec   Codec        `json:"codec"`
	TrackID string       `json:"track_id"`
	Size    int64        `json:"size"`
	Units   []Unit       `json:"units"`
	Issues  media.Issues `json:"issues"`
}

// Keyframes returns valid, complete keyframe units in stream order.
func (r *Result) Keyframes() []Unit {
	var out []Unit
	for _, u := range r.Units {
		if u.Keyframe && u.Complete {
			out = append(out, u)
		}
	}
	return out
}

// Parser is a codec-specific streaming parser.
type Parser func(r io.Reader, trackID string) (*Result, error)

var parsers = map[Codec]Parser{}

// RegisterParser adds a codec parser. It is intended for package init and
// tests; codec packages in this module register themselves.
func RegisterParser(codec Codec, p Parser) { parsers[codec] = p }

// Scan parses a supported elementary stream.
func Scan(r io.Reader, codec Codec, trackID string) (*Result, error) {
	p, ok := parsers[codec]
	if !ok {
		return nil, media.NewError(media.CatInvalidArgument, trackID, -1, -1, "unsupported codec %q", codec)
	}
	return p(r, trackID)
}

type byteScanner struct {
	r      *bufio.Reader
	offset int64
}

func newByteScanner(r io.Reader) *byteScanner {
	if br, ok := r.(*bufio.Reader); ok {
		return &byteScanner{r: br}
	}
	return &byteScanner{r: bufio.NewReaderSize(r, 64*1024)}
}

// readAt returns the byte and its zero-based offset.
func (s *byteScanner) readAt() (byte, int64, error) {
	off := s.offset
	b, err := s.r.ReadByte()
	if err != nil {
		return 0, off, err
	}
	s.offset++
	return b, off, nil
}

type classifiedNAL struct {
	kind     string
	keyframe bool
	issueCat media.Category
	issueMsg string
}

type nalClassifier func(header []byte, length int64) classifiedNAL

// scanAnnexB implements shared start-code boundary detection for H.264/H.265.
func scanAnnexB(r io.Reader, track string, codec Codec, headerSize int, classify nalClassifier) (*Result, error) {
	res := &Result{Codec: codec, TrackID: track}
	s := newByteScanner(r)

	// Locate the first start code. Leading bytes are not valid Annex B.
	start, headerOff, scLen, err := findStartCode(s)
	if err != nil {
		if err == io.EOF {
			res.Size = s.offset
			if start >= 0 {
				res.Issues.Add(media.CatTruncatedNAL, track, int64(len(res.Units)), start, "%s stream ends inside start code", codec)
			}
			return res, nil
		}
		return nil, err
	}

	for {
		u := Unit{Index: len(res.Units), Codec: codec, Offset: start, HeaderOffset: headerOff, PayloadOffset: headerOff}
		header := make([]byte, 0, headerSize)
		for len(header) < headerSize {
			b, off, readErr := s.readAt()
			if readErr != nil {
				u.End = s.offset
				u.Length = u.End - u.Offset
				u.Complete = false
				res.Units = append(res.Units, u)
				res.Issues.Add(media.CatTruncatedNAL, track, int64(u.Index), start, "%s NAL header truncated at byte %d", codec, off)
				res.Size = s.offset
				return res, nil
			}
			header = append(header, b)
		}

		nextStart, nextHeader, nextLen, scanErr := scanUntilStartCode(s)
		if scanErr != nil && scanErr != io.EOF {
			return nil, scanErr
		}
		if scanErr == io.EOF {
			u.End = s.offset
			u.Length = u.End - u.Offset
			c := classify(header, u.End-headerOff)
			applyClassified(&u, c)
			if !u.Complete {
				res.Issues.Add(c.issueCat, track, int64(u.Index), u.Offset, "%s", c.issueMsg)
			}
			res.Units = append(res.Units, u)
			res.Size = s.offset
			return res, nil
		}

		_ = nextLen
		u.End = nextStart
		u.Length = u.End - u.Offset
		c := classify(header, nextStart-headerOff)
		applyClassified(&u, c)
		if !u.Complete {
			res.Issues.Add(c.issueCat, track, int64(u.Index), u.Offset, "%s", c.issueMsg)
		}
		res.Units = append(res.Units, u)

		start, headerOff, scLen = nextStart, nextHeader, nextLen
		_ = scLen
	}
}

func applyClassified(u *Unit, c classifiedNAL) {
	u.Kind = c.kind
	u.Keyframe = c.keyframe
	u.Complete = c.issueCat == ""
}

// findStartCode consumes through the first 00 00 01/00 00 00 01 sequence.
// It returns start offset, first payload offset, and start-code length.
func findStartCode(s *byteScanner) (start, payload int64, length int, err error) {
	zeros := 0
	for {
		b, off, err := s.readAt()
		if err != nil {
			return -1, -1, 0, err
		}
		if b == 0 {
			zeros++
			continue
		}
		if b == 1 && zeros >= 2 {
			if zeros >= 3 {
				return off - 3, off + 1, 4, nil
			}
			return off - 2, off + 1, 3, nil
		}
		zeros = 0
	}
}

// scanUntilStartCode scans NAL payload bytes and returns the next start code.
func scanUntilStartCode(s *byteScanner) (start, payload int64, length int, err error) {
	zeros := 0
	for {
		b, off, readErr := s.readAt()
		if readErr != nil {
			return -1, -1, 0, readErr
		}
		if b == 0 {
			zeros++
			continue
		}
		if b == 1 && zeros >= 2 {
			if zeros >= 3 {
				start, length = off-3, 4
			} else {
				start, length = off-2, 3
			}
			return start, off + 1, length, nil
		}
		zeros = 0
	}
}
