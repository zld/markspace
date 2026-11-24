package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"mediad/internal/ingest"
)

// outf writes a formatted line to stdout.
func outf(format string, a ...any) {
	fmt.Printf(format+"\n", a...)
}

// countFrames walks a stream purely to validate framing; the first failure is
// returned as a *ParseError carrying the raw offset.
func countFrames(raw []byte) (int, error) {
	rd := ingest.NewReader(bytes.NewReader(raw))
	n := 0
	for {
		f, err := rd.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return n, nil
			}
			return n, err
		}
		switch f.Type {
		case ingest.TagRTP:
			if _, err := ingest.ParseRTP(f); err != nil {
				return n, err
			}
		case ingest.TagRTCP:
			if _, err := ingest.ParseRTCP(f); err != nil {
				return n, err
			}
		}
		n++
	}
}
