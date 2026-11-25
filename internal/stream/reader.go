package stream

import (
	"io"

	"mediad/internal/errors"
)

// NALUnit is one start-code-delimited unit (Annex B NAL or AV1 OBU).
type NALUnit struct {
	Offset    int64   `json:"offset"` // absolute offset of the start code
	Info      NALInfo `json:"-"`      // validated header info
	Type      uint8   `json:"type"`   // syntax-level type code
	Data      []byte  `json:"-"`      // payload copy, capped at MaxNALBytes
	BadHeader bool    `json:"bad_header"`
	Truncated bool    `json:"truncated"`
	Oversized bool    `json:"oversized"`
}

// AccessUnit is a group of NAL units sharing one presentation time.
type AccessUnit struct {
	Index    uint64    `json:"index"`
	Start    int64     `json:"start_offset"`
	End      int64     `json:"end_offset"`
	NALs     []NALUnit `json:"-"`
	Keyframe bool      `json:"keyframe"`
	Corrupt  bool      `json:"corrupt"`
	Reasons  []string  `json:"reasons,omitempty"`
}

// Damage is a structural defect located at a stream byte offset.
type Damage struct {
	Offset int64     `json:"offset"`
	Code   errs.Code `json:"code"`
	Msg    string    `json:"msg"`
}

// Config bounds every memory queue used by the scanner. Zero values fall
// back to defaults so memory stays bounded regardless of input length.
type Config struct {
	BlockSize    int   // bytes read per io.Reader call
	MaxAUBytes   int64 // access-unit byte-size cap
	MaxNALBytes  int   // per-NAL payload copy cap
	MaxTotalRead int64 // whole-stream read budget, 0 = unlimited
}

// DefaultConfig returns conservative bounds suitable for local fixtures.
func DefaultConfig() Config {
	return Config{BlockSize: 64 * 1024, MaxAUBytes: 8 << 20, MaxNALBytes: 8 << 20}
}

func (c Config) withDefaults() Config {
	d := DefaultConfig()
	if c.BlockSize <= 0 {
		c.BlockSize = d.BlockSize
	}
	if c.MaxAUBytes <= 0 {
		c.MaxAUBytes = d.MaxAUBytes
	}
	if c.MaxNALBytes <= 0 {
		c.MaxNALBytes = d.MaxNALBytes
	}
	return c
}

// Scanner is a streaming Annex B scanner with bounded memory. A byte-at-a-
// time start-code state machine runs over bounded block reads; the only
// retained data are one block and the capped payload of the open NAL.
type Scanner struct {
	r     io.Reader
	codec Codec
	cfg   Config

	block []byte
	total int64
	done  bool

	zeros int // run length of consecutive zero bytes, capped at 3

	inNAL     bool
	nalOff    int64
	payload   []byte
	nalOver   bool
	nalBytes  int64
	classOpen bool // header of the open NAL still needs classification

	au      *AccessUnit
	auIndex uint64
	queued  []*AccessUnit
	damages []Damage
}

// NewScanner validates configuration and wraps r as a bounded scanner.
func NewScanner(r io.Reader, codec Codec, cfg Config) (*Scanner, error) {
	if _, err := ParseCodec(string(codec)); err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()
	if cfg.BlockSize < 4 {
		return nil, errs.New(errs.Fatal, errs.CodeInternal, "block size must be >= 4")
	}
	return &Scanner{r: r, codec: codec, cfg: cfg, block: make([]byte, cfg.BlockSize)}, nil
}

// Damages returns every structural defect observed so far.
func (s *Scanner) Damages() []Damage { return s.damages }

// TotalRead returns the number of stream bytes consumed.
func (s *Scanner) TotalRead() int64 { return s.total }

// Next returns the next complete access unit, or (nil, nil) at end of input.
func (s *Scanner) Next() (*AccessUnit, error) {
	if s.done && len(s.queued) == 0 {
		return nil, nil
	}
	for len(s.queued) == 0 {
		nr, err := s.r.Read(s.block)
		for i := 0; i < nr; i++ {
			s.feed(s.block[i], s.total+int64(i))
		}
		s.total += int64(nr)
		if s.cfg.MaxTotalRead > 0 && s.total > s.cfg.MaxTotalRead {
			return nil, errs.New(errs.Fatal, errs.CodeIOFailure,
				"read budget %d exceeded", s.cfg.MaxTotalRead)
		}
		if len(s.queued) > 0 {
			break
		}
		if err == io.EOF {
			return s.finishEOF()
		}
		if err != nil {
			return nil, errs.New(errs.Fatal, errs.CodeIOFailure, "read failed: %v", err)
		}
	}
	out := s.queued[0]
	s.queued = s.queued[1:]
	return out, nil
}

// feed runs one byte through the start-code state machine.
func (s *Scanner) feed(b byte, absOff int64) {
	if b == 0 {
		if s.zeros < 3 {
			s.zeros++
		}
		if s.inNAL {
			s.appendPayload(b)
		}
		return
	}
	if b == 1 && s.zeros >= 2 {
		prefixLen := s.zeros // 2 (00 00 01) or 3 (00 00 00 01)
		s.zeros = 0
		s.onStartCode(absOff-int64(prefixLen), prefixLen)
		return
	}
	s.zeros = 0
	if s.inNAL {
		s.appendPayload(b)
	}
}

// onStartCode closes the previous NAL and opens a new one. The access-unit
// boundary is resolved once the new NAL's header bytes are available.
func (s *Scanner) onStartCode(prefixStart int64, prefixLen int) {
	if s.inNAL {
		s.closeNAL(prefixStart, prefixLen)
	}
	if s.au == nil {
		s.au = &AccessUnit{Index: s.auIndex, Start: prefixStart}
		s.auIndex++
	}
	s.inNAL = true
	s.nalOff = prefixStart
	s.payload = s.payload[:0]
	s.nalOver, s.nalBytes = false, 0
	s.classOpen = true
}

func (s *Scanner) appendPayload(b byte) {
	s.nalBytes++
	if len(s.payload) >= s.cfg.MaxNALBytes {
		s.nalOver = true
		return
	}
	s.payload = append(s.payload, b)
	if s.classOpen && len(s.payload) >= s.headerNeed() {
		s.classOpen = false
		s.classifyOpenNAL()
	}
}

func (s *Scanner) headerNeed() int {
	if s.codec == HEVC {
		return 2
	}
	return 1
}

// classifyOpenNAL applies the framing rule: an AUD, or a VCL NAL following
// an earlier VCL NAL, begins a fresh access unit.
func (s *Scanner) classifyOpenNAL() {
	info, _ := s.codec.InspectNAL(s.payload)
	boundary := false
	switch info.Class {
	case NALAUD:
		boundary = len(s.au.NALs) > 0
	case NALVCL:
		for _, n := range s.au.NALs {
			if !n.BadHeader && n.Info.Class == NALVCL {
				boundary = true
				break
			}
		}
	}
	if boundary {
		s.queued = append(s.queued, s.au)
		s.au = &AccessUnit{Index: s.auIndex, Start: s.nalOff}
		s.auIndex++
	}
}

// closeNAL validates and stores the NAL ending at endOff (exclusive). The
// prefixLen trailing zero bytes belong to the next start code, not the payload.
func (s *Scanner) closeNAL(endOff int64, prefixLen int) {
	if len(s.payload) >= prefixLen {
		s.payload = s.payload[:len(s.payload)-prefixLen]
	}
	unit := NALUnit{Offset: s.nalOff, Oversized: s.nalOver}
	info, err := s.codec.InspectNAL(s.payload)
	if err != nil {
		unit.BadHeader = true
		s.record(unit.Offset, err)
	} else {
		unit.Info, unit.Type = info, info.Type
		if len(s.payload) < info.MinPayload {
			unit.Truncated = true
			s.record(unit.Offset, errs.At(errs.Recoverable,
				errs.CodeTruncatedStream, unit.Offset,
				"payload %d bytes < minimum %d", len(s.payload), info.MinPayload))
		}
	}
	if s.nalOver || s.nalBytes-int64(prefixLen) > s.cfg.MaxAUBytes {
		unit.Oversized = true
		s.record(unit.Offset, errs.At(errs.Recoverable,
			errs.CodeOversizedAU, unit.Offset,
			"NAL exceeds cap (%d bytes)", s.nalBytes))
	}
	unit.Data = append([]byte(nil), s.payload...)
	s.au.NALs = append(s.au.NALs, unit)
	s.au.End = endOff
	s.au.Corrupt = s.au.Corrupt || unit.BadHeader || unit.Truncated || unit.Oversized
	if unit.Info.Keyframe {
		s.au.Keyframe = true
	}
	s.inNAL = false
}

func (s *Scanner) record(off int64, err error) {
	e := errs.As(err)
	s.damages = append(s.damages, Damage{Offset: off, Code: e.Code, Msg: e.Msg})
	if s.au != nil {
		s.au.Corrupt = true
		s.au.Reasons = append(s.au.Reasons, string(e.Code))
	}
}

func (s *Scanner) finishEOF() (*AccessUnit, error) {
	if s.inNAL {
		s.closeNAL(s.total, 0)
	}
	if s.au != nil {
		if s.au.End == 0 {
			s.au.End = s.total
		}
		s.queued = append(s.queued, s.au)
		s.au = nil
	}
	if len(s.queued) == 0 && s.total > 0 {
		// Bytes were consumed but no start code ever appeared.
		s.record(0, errs.At(errs.Recoverable, errs.CodeTruncatedStream,
			0, "no start code found in %d bytes", s.total))
	}
	s.done = true
	if len(s.queued) == 0 {
		return nil, nil
	}
	out := s.queued[0]
	s.queued = s.queued[1:]
	return out, nil
}
