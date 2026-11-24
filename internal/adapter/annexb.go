package adapter

import (
	"bytes"

	"archive-recovery/internal/media"
)

// AnnexBScanner is a streaming start-code (0x000001 / 0x00000001) splitter
// shared by the H.264 and H.265 adapters. Classify receives the NAL header
// bytes (at least two) and decides frame/keyframe semantics per codec.
type AnnexBScanner struct {
	classify    func(nal []byte) (typeCode int, isFrame, isKey bool)
	maxUnitSize int64

	buf       bytes.Buffer
	absPos    int64 // absolute offset of buf[0]
	unitStart int64 // absolute offset of the open unit's start code
	haveUnit  bool
}

// NewAnnexBScanner builds a scanner bound to a codec-specific classifier.
func NewAnnexBScanner(classify func(nal []byte) (int, bool, bool), maxUnitSize int64) *AnnexBScanner {
	return &AnnexBScanner{classify: classify, maxUnitSize: maxUnitSize}
}

// Write ingests one chunk and emits every unit fully contained in it.
func (s *AnnexBScanner) Write(p []byte) ([]Unit, []media.UnitError, error) {
	if len(p) == 0 {
		return nil, nil, nil
	}
	s.buf.Write(p)
	var units []Unit
	var uerrs []media.UnitError

	data := s.buf.Bytes()
	searchFrom := 0
	if s.haveUnit { // skip the start code that opens the current unit
		_, scLen := findStartCode(data)
		searchFrom = scLen
	}
	for {
		rel, scLen := findStartCode(data[searchFrom:])
		if rel < 0 {
			break
		}
		rel += searchFrom
		scAbs := s.absPos + int64(rel)
		if s.haveUnit {
			if u := s.emitUnit(scAbs, data, &uerrs); u != nil {
				units = append(units, *u)
			}
		}
		s.unitStart = scAbs
		s.haveUnit = true
		searchFrom = rel + scLen
	}

	keepFrom := 0
	switch {
	case s.haveUnit:
		keepFrom = int(s.unitStart - s.absPos)
	case s.buf.Len() > 3:
		keepFrom = s.buf.Len() - 3
	}
	if keepFrom > 0 {
		s.buf.Next(keepFrom)
		s.absPos += int64(keepFrom)
	}
	if int64(s.buf.Len()) > s.maxUnitSize {
		return units, uerrs, media.NewOffsetError(media.ClassRecoverable, "adapter",
			"NAL unit exceeds maximum size", s.unitStart)
	}
	return units, uerrs, nil
}

// Close finalizes the last open unit or reports a dangling one.
func (s *AnnexBScanner) Close() ([]Unit, *media.UnitError) {
	if !s.haveUnit {
		s.buf.Reset()
		return nil, nil
	}
	raw := s.buf.Bytes()
	unitRel := s.unitStart - s.absPos
	nal := raw[unitRel:]
	_, scLen := findStartCode(nal)
	if scLen == 0 || len(nal) < scLen+2 {
		dangling := &media.UnitError{
			Offset: s.unitStart, Length: int64(len(nal)),
			Class: media.ClassRecoverable, Reason: "truncated NAL unit",
		}
		s.haveUnit = false
		s.buf.Reset()
		return nil, dangling
	}
	typeCode, isFrame, isKey := s.classify(nal[scLen:])
	unit := Unit{
		Offset: s.unitStart, Length: int64(len(nal)),
		TypeCode: typeCode, IsFrame: isFrame, IsKeyframe: isKey,
	}
	s.haveUnit = false
	s.buf.Reset()
	return []Unit{unit}, nil
}

func (s *AnnexBScanner) emitUnit(nextStart int64, window []byte, uerrs *[]media.UnitError) *Unit {
	from := s.unitStart - s.absPos
	to := nextStart - s.absPos
	raw := window[from:to]
	_, scLen := findStartCode(raw)
	length := nextStart - s.unitStart
	if scLen == 0 || len(raw) < scLen+2 {
		*uerrs = append(*uerrs, media.UnitError{
			Offset: s.unitStart, Length: length,
			Class: media.ClassRecoverable, Reason: "truncated NAL unit",
		})
		return nil
	}
	typeCode, isFrame, isKey := s.classify(raw[scLen:])
	return &Unit{
		Offset: s.unitStart, Length: length,
		TypeCode: typeCode, IsFrame: isFrame, IsKeyframe: isKey,
	}
}

// findStartCode returns the relative offset and length (3 or 4) of the first
// Annex B start code in p, or -1 when none is present.
func findStartCode(p []byte) (int, int) {
	for i := 0; i+2 < len(p); i++ {
		if p[i] == 0 && p[i+1] == 0 && p[i+2] == 1 {
			if i > 0 && p[i-1] == 0 {
				return i - 1, 4
			}
			return i, 3
		}
	}
	return -1, 0
}
