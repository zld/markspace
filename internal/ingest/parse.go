package ingest

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"medpipe/internal/clock"
	"medpipe/internal/fault"
)

// Options controls streaming parsing limits.
type Options struct {
	MaxFrameBytes int64
}

// Result contains validated frames and isolated failures.
type Result struct {
	Frames    []*Frame
	Failures  []fault.Failure
	BytesRead int64
	// Recovered counts recoverable failures followed by a valid record.
	Recovered int
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// ParseStream parses concatenated frame records. Checksum and metadata
// failures are isolated and parsing continues at the next length-delimited
// record when enough framing information is available.
func ParseStream(r io.Reader, opts Options) (*Result, error) {
	if r == nil {
		return nil, errors.New("nil reader")
	}
	maxBytes := opts.MaxFrameBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxFrameBytes
	}
	cr := &countingReader{r: r}
	out := &Result{}
	pendingRecovery := -1

	markRecovery := func() {
		if pendingRecovery >= 0 {
			out.Failures[pendingRecovery].Recovered = true
			out.Recovered++
			pendingRecovery = -1
		}
	}

	for {
		start := cr.n
		var magic [4]byte
		n, err := io.ReadFull(cr, magic[:])
		if errors.Is(err, io.EOF) && n == 0 {
			markRecovery()
			out.BytesRead = cr.n
			return out, nil
		}
		if err != nil {
			out.addTruncated(start, -1, "magic", err)
			out.BytesRead = cr.n
			return out, nil
		}
		if string(magic[:]) != FrameMagic {
			fe := fault.New(fault.StageParse, "bad_magic", start, -1, fmt.Errorf("magic %q", magic))
			out.Failures = append(out.Failures, failure(fe))
			out.BytesRead = cr.n
			return out, nil
		}

		var h [FrameHeaderSize - 4]byte
		if _, err := io.ReadFull(cr, h[:]); err != nil {
			out.addTruncated(start, -1, "header", err)
			out.BytesRead = cr.n
			return out, nil
		}
		track := int(binary.BigEndian.Uint16(h[2:4]))
		rec, fe := parseHeader(start, h[:], maxBytes)
		if fe != nil {
			out.Failures = append(out.Failures, failure(fe))
			pendingRecovery = len(out.Failures) - 1
			if rec != nil && rec.pixelLen > 0 {
				if !drainPixels(cr, rec.pixelLen, out) {
					out.BytesRead = cr.n
					return out, nil
				}
				continue
			}
			out.BytesRead = cr.n
			return out, nil
		}

		pixels := make([]byte, rec.pixelLen)
		if _, err := io.ReadFull(cr, pixels); err != nil {
			out.addTruncated(start, track, "pixels", err)
			out.BytesRead = cr.n
			return out, nil
		}
		gotSum := sha256.Sum256(pixels)
		if gotSum != rec.checksum {
			fe := fault.New(fault.StageParse, "checksum_mismatch", start, track,
				fmt.Errorf("expected %x, got %x", rec.checksum, gotSum))
			out.Failures = append(out.Failures, failure(fe))
			pendingRecovery = len(out.Failures) - 1
			continue
		}

		markRecovery()
		out.Frames = append(out.Frames, &Frame{
			TrackID: rec.trackID, PTS: rec.pts, Timebase: rec.tb,
			Width: rec.width, Height: rec.height, KeyFrame: rec.key,
			PixelFormat: rec.pixFmt, Color: rec.color, Pixels: pixels,
			SHA256: rec.checksum, Offset: start,
		})
	}
}

type headerRecord struct {
	trackID  uint16
	pts      int64
	tb       clock.Timebase
	width    int
	height   int
	key      bool
	pixFmt   uint8
	color    ColorMetadata
	pixelLen int64
	checksum [32]byte
}

func parseHeader(start int64, h []byte, maxBytes int64) (*headerRecord, *fault.Error) {
	trackID := binary.BigEndian.Uint16(h[2:4]) // absolute 6:8
	track := int(trackID)
	if h[0] != 1 {
		return nil, fault.New(fault.StageParse, "unsupported_version", start, track, fmt.Errorf("version %d", h[0]))
	}
	pts := int64(binary.BigEndian.Uint64(h[4:12])) // absolute 8:16
	tb := clock.Timebase{
		Numerator:   int64(binary.BigEndian.Uint32(h[12:16])),
		Denominator: int64(binary.BigEndian.Uint32(h[16:20])),
	}
	if err := tb.Validate(); err != nil {
		return nil, fault.New(fault.StageParse, "invalid_timebase", start, track, err)
	}
	width := int(binary.BigEndian.Uint16(h[20:22]))  // abs24:26
	height := int(binary.BigEndian.Uint16(h[22:24])) // abs26:28
	pixFmt := h[24]
	color := ColorMetadata{Primaries: h[25], Transfer: h[26], Matrix: h[27], FullRange: h[28]&1 == 1}
	pixelLen := int64(binary.BigEndian.Uint32(h[32:36]))
	var sum [32]byte
	copy(sum[:], h[36:68])

	rec := &headerRecord{trackID: trackID, pts: pts, tb: tb, width: width, height: height,
		key: h[1]&1 == 1, pixFmt: pixFmt, color: color, pixelLen: pixelLen, checksum: sum}
	if width <= 0 || height <= 0 || width > MaxDimension || height > MaxDimension {
		return rec, fault.New(fault.StageParse, "invalid_dimensions", start, track, fmt.Errorf("%dx%d", width, height))
	}
	if pixFmt != PixelRGB24 {
		return rec, fault.New(fault.StageParse, "unsupported_pixel_format", start, track, fmt.Errorf("%d", pixFmt))
	}
	want, err := pixelLength(width, height)
	if err != nil {
		return rec, fault.New(fault.StageParse, "invalid_dimensions", start, track, err)
	}
	if pixelLen > maxBytes {
		return rec, fault.New(fault.StageParse, "frame_too_large", start, track,
			fmt.Errorf("pixel length %d exceeds limit %d", pixelLen, maxBytes))
	}
	if pixelLen != want {
		return rec, fault.New(fault.StageParse, "invalid_pixel_length", start, track,
			fmt.Errorf("length %d, expected %d", pixelLen, want))
	}
	return rec, nil
}

func drainPixels(cr *countingReader, n int64, out *Result) bool {
	if n <= 0 {
		return true
	}
	if _, err := io.CopyN(io.Discard, cr, n); err != nil {
		out.addTruncated(cr.n, -1, "invalid-record-pixels", err)
		return false
	}
	return true
}

func (r *Result) addTruncated(offset int64, track int, field string, err error) {
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		err = fmt.Errorf("%s: unexpected EOF", field)
	}
	r.Failures = append(r.Failures, fault.Failure{
		Stage: fault.StageParse, Code: "truncated_" + field, Offset: offset,
		TrackID: track, Message: err.Error(),
	})
}

func failure(fe *fault.Error) fault.Failure {
	f, _ := fault.As(fe)
	return f
}

// ParseBytes is a convenience wrapper for tests and small fixtures.
func ParseBytes(data []byte, opts Options) (*Result, error) {
	return ParseStream(bytes.NewReader(data), opts)
}
