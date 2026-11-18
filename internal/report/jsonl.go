// Package report writes JSON Lines output for jitterlab runs.
//
// Every run emits one "frame" object per playout slot followed by a single
// "summary" object. Lines are streamed so long runs are not held in memory.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"jitterlab/internal/metrics"
)

// Line kinds written to the output.
const (
	KindFrame   = "frame"
	KindSummary = "summary"
)

// FrameLine is one per-slot record on the wire.
type FrameLine struct {
	Type  string        `json:"type"`
	Frame metrics.Frame `json:"frame"`
}

// SummaryLine is the final aggregate on the wire.
type SummaryLine struct {
	Type    string          `json:"type"`
	Summary metrics.Summary `json:"summary"`
}

// Writer streams JSON Lines to an underlying writer.
type Writer struct {
	enc *json.Encoder
}

// New wraps w. A nil writer is rejected.
func New(w io.Writer) (*Writer, error) {
	if w == nil {
		return nil, fmt.Errorf("report: nil writer")
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return &Writer{enc: enc}, nil
}

// Create opens path for writing, or returns stdout when path is empty or
// "-". The returned closer must be closed by the caller; it is a no-op
// for stdout.
func Create(path string) (*Writer, io.Closer, error) {
	if path == "" || path == "-" {
		w, err := New(os.Stdout)
		if err != nil {
			return nil, nil, err
		}
		return w, nopCloser{}, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("report: open %s: %w", path, err)
	}
	w, err := New(f)
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return w, f, nil
}

// WriteFrame emits one frame line.
func (w *Writer) WriteFrame(f metrics.Frame) error {
	return w.enc.Encode(FrameLine{Type: KindFrame, Frame: f})
}

// WriteSummary emits the summary line.
func (w *Writer) WriteSummary(s metrics.Summary) error {
	return w.enc.Encode(SummaryLine{Type: KindSummary, Summary: s})
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }
