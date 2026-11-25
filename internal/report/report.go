// Package report defines the JSON-lines protocol the CLI writes to stdout.
// Every record is a single line with a "kind" discriminator so terminal
// consumers can filter without parsing prose.
package report

import (
	"encoding/json"
	"io"
	"sync"
)

// Kinds of report lines.
const (
	KindStage    = "stage"
	KindFixture  = "fixture"
	KindTimeline = "timeline"
	KindSplit    = "split"
	KindDerive   = "derive"
	KindManifest = "manifest"
	KindVerify   = "verify"
	KindResume   = "resume"
	KindDiff     = "diff"
	KindError    = "error"
	KindSummary  = "summary"
)

// Line is the envelope; Payload carries the typed record.
type Line struct {
	Kind    string `json:"kind"`
	Payload any    `json:"payload"`
}

// Writer serializes report lines. It is safe for concurrent use and writes
// each line whole so a crash still leaves a readable prefix.
type Writer struct {
	mu sync.Mutex
	w  io.Writer
}

func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

// Emit writes one JSON line. Encoding failures are swallowed: a report that
// cannot be encoded must not take down a run that already succeeded.
func (w *Writer) Emit(kind string, payload any) {
	b, err := json.Marshal(Line{Kind: kind, Payload: payload})
	if err != nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.w.Write(b)
	w.w.Write([]byte("\n"))
}
