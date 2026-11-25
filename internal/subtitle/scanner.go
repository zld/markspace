package subtitle

import (
	"bytes"

	"archivecheck/internal/diagnostic"
)

type sourceLine struct {
	text  string
	start int64
	eof   bool
}

type lineScanner struct {
	data   []byte
	pos    int
	sample int
	limits Limits
}

func (s *lineScanner) next() (sourceLine, error) {
	if s.pos >= len(s.data) {
		return sourceLine{start: int64(s.pos), eof: true}, nil
	}
	start := s.pos
	rest := s.data[start:]
	nl := bytes.IndexByte(rest, '\n')

	var contentEnd, nextPos int
	eof := false
	if nl < 0 {
		contentEnd = len(s.data)
		nextPos = contentEnd
		eof = true
	} else {
		lf := start + nl
		contentEnd = lf
		if lf > start && s.data[lf-1] == '\r' {
			contentEnd = lf - 1
		}
		nextPos = lf + 1
	}

	lineLength := contentEnd - start
	if lineLength > s.limits.MaxLineBytes {
		offset := int64(start + s.limits.MaxLineBytes)
		return sourceLine{start: int64(start), eof: eof}, diagnostic.New(
			diagnostic.CodeLineTooLarge, diagnostic.PhaseSubtitle, s.sample, offset,
			"subtitle line length %d exceeds limit %d", lineLength, s.limits.MaxLineBytes,
		)
	}

	line := sourceLine{text: string(s.data[start:contentEnd]), start: int64(start), eof: eof}
	s.pos = nextPos
	return line, nil
}
