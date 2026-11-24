package medpipe

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/pion/rtp"
)

// maxBufferedFrames caps per-track reassembly memory: once the pending frame
// queue reaches this depth the scanner fails instead of growing without
// bound.
const maxBufferedFrames = 64

// ScanOptions parameterizes one streaming scan.
type ScanOptions struct {
	Track     string
	Codec     Codec
	Kind      Kind
	ClockRate uint32
	// ExpectedDelta seeds the clock with the nominal per-sample RTP tick
	// delta; zero means the clock learns it from the first clean pair.
	ExpectedDelta uint64
	// ChunkSize bounds the read buffer used per record.
	ChunkSize int
}

// UnitEvent is one complete frame plus its normalized timing.
type UnitEvent struct {
	Frame  Frame
	Sample SampleRecord
}

// ScanResult is the full output of a scan: complete units, recoverable and
// fatal errors, and clock statistics. Truncated units never produce entries.
type ScanResult struct {
	Units      []UnitEvent      `json:"-"`
	Errors     []*PipelineError `json:"errors"`
	ClockStats ClockStats       `json:"clock_stats"`
	Records    []SampleRecord   `json:"records"`
	BytesRead  int64            `json:"bytes_read"`
}

// Scanner streams one capture file with bounded memory. It is safe for
// sequential use by a single goroutine.
type Scanner struct {
	opts   ScanOptions
	r      *bufio.Reader
	clock  *Clock
	b      *ErrorBuilder
	result ScanResult
	// pending holds payloads belonging to the not-yet-finalized access unit.
	pending  [][]byte
	pendOff  int64
	pendSeq  uint16
	pendTS   uint32
	havePend bool
	pendMeta *FrameMeta
}

// NewCaptureScanner verifies the MPR1 magic on r and returns a scanner
// positioned at the first length-prefixed record. The returned error slice
// is non-nil with a fatal entry on a bad or missing magic.
func NewCaptureScanner(r io.Reader, opts ScanOptions) (*Scanner, []*PipelineError) {
	if errs := VerifyMagic(r, opts.Track); len(errs) > 0 {
		return nil, errs
	}
	s := NewScanner(r, opts)
	// Offsets reported in errors and index entries are absolute in the
	// capture file; account for the four magic bytes already consumed.
	s.result.BytesRead = int64(len(CaptureMagic))
	return s, nil
}

// NewScanner wraps r. The magic header must already have been consumed by the
// caller (see NewCaptureScanner); the scanner starts at the first length
// prefix.
func NewScanner(r io.Reader, opts ScanOptions) *Scanner {
	if opts.ChunkSize <= 0 {
		opts.ChunkSize = 64 * 1024
	}
	return &Scanner{
		opts:  opts,
		r:     bufio.NewReaderSize(r, opts.ChunkSize),
		clock: NewClock(opts.Track, opts.ClockRate, opts.ExpectedDelta),
		b:     NewErrorBuilder(opts.Track),
	}
}

// VerifyMagic reads and validates the 4-byte capture magic, returning a fatal
// error anchored at offset zero on mismatch.
func VerifyMagic(r io.Reader, track string) []*PipelineError {
	var got [4]byte
	if _, err := io.ReadFull(r, got[:]); err != nil {
		return []*PipelineError{NewErrorBuilder(track).FatalAt(CodeInvalidRTP,
			"capture file too short for magic header",
			"regenerate the capture; the input is not a medpipe .rtp file", 0)}
	}
	if got != CaptureMagic {
		return []*PipelineError{NewErrorBuilder(track).FatalAt(CodeInvalidRTP,
			fmt.Sprintf("bad capture magic % x, want % x", got, CaptureMagic),
			"provide a medpipe RTP capture (MPR1)", 0)}
	}
	return nil
}

// readRecord reads one length-prefixed RTP packet. A short read at a record
// boundary is a truncated trailing unit and is reported, never fabricated.
func (s *Scanner) readRecord() (*rtp.Packet, int64, error) {
	off := s.result.BytesRead
	var lenBuf [2]byte
	n, err := io.ReadFull(s.r, lenBuf[:])
	s.result.BytesRead += int64(n)
	if err == io.EOF || errors.Is(err, io.ErrUnexpectedEOF) && n == 0 {
		return nil, off, io.EOF // clean end between records
	}
	if err != nil {
		return nil, off, err
	}
	length := int(binary.BigEndian.Uint16(lenBuf[:]))
	if length == 0 || length > maxRecordSize {
		return nil, off, s.b.FatalAt(CodeInvalidRTP,
			fmt.Sprintf("illegal record length %d at offset %d", length, off),
			"the capture index is corrupt; regenerate the stream", off)
	}
	buf := make([]byte, length)
	m, err := io.ReadFull(s.r, buf)
	s.result.BytesRead += int64(m)
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		// Truncated trailing record: keep its exact offset, emit no unit.
		s.result.Errors = append(s.result.Errors, s.b.RecoverableAt(CodeTruncatedUnit,
			fmt.Sprintf("trailing RTP record declares %d bytes but only %d remain", length, m),
			"re-capture or trim the incomplete tail before re-running", off))
		return nil, off, io.EOF
	}
	if err != nil {
		return nil, off, err
	}
	pkt := &rtp.Packet{}
	if err := pkt.Unmarshal(buf); err != nil {
		return nil, off, s.b.RecoverableAt(CodeInvalidRTP,
			"malformed RTP packet: "+err.Error(),
			"drop the packet at the capture stage", off)
	}
	return pkt, off, nil
}

// Scan drains the whole reader and returns the assembled result. The scanner
// stops early only on a fatal error; recoverable errors are collected.
func (s *Scanner) Scan() (ScanResult, error) {
	for {
		pkt, off, err := s.readRecord()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if pe, ok := err.(*PipelineError); ok && pe.Kind == KindFatal {
				s.result.Errors = append(s.result.Errors, pe)
				return s.result, err
			}
			return s.result, err
		}
		if pkt == nil {
			continue // recoverable packet-level error already recorded
		}
		if stop := s.ingest(pkt, off); stop != nil {
			s.result.Errors = append(s.result.Errors, stop)
			return s.result, stop
		}
	}
	if perr := s.flushPending(); perr != nil {
		s.result.Errors = append(s.result.Errors, perr)
		return s.result, perr
	}
	stats, recs, _ := s.clock.Finalize()
	s.result.ClockStats = stats
	s.result.Records = recs
	if len(s.result.Units) == 0 && len(s.result.Errors) == 0 {
		s.result.Errors = append(s.result.Errors, s.b.Recoverable(CodeEmptyStream,
			"capture contains no RTP records",
			"verify the producer; an empty track produces an empty, reusable index"))
	}
	return s.result, nil
}
