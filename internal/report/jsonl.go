// Package report emits machine-readable JSON Lines output: one record per
// cue followed by a single summary record.
package report

import (
	"bufio"
	"encoding/json"
	"io"
	"time"

	"subsync/internal/check"
	"subsync/internal/timeline"
)

// CueRecord is the per-cue JSONL object. Nullable fields use pointers so that
// unmatched cues emit explicit null values rather than misleading zeros.
type CueRecord struct {
	Type             string `json:"type"`
	CueID            string `json:"cue_id"`
	StartFrame       int64  `json:"start_frame"`
	EndFrame         int64  `json:"end_frame"`
	RoundtripDeltaMS *int64 `json:"roundtrip_delta_ms"`
	DroppedCount     int    `json:"dropped_count"`
	Issue            string `json:"issue,omitempty"`
}

// SummaryRecord is the final JSONL object describing the whole run.
type SummaryRecord struct {
	Type         string `json:"type"`
	InputCount   int    `json:"input_count"`
	OutputCount  int    `json:"output_count"`
	LostCount    int    `json:"lost_count"`
	OverlapCount int    `json:"overlap_count"`
	StyleLoss    int    `json:"style_loss"`
	Encoding     string `json:"encoding"`
	MaxDeltaMS   int64  `json:"max_roundtrip_delta_ms"`
}

// Builder accumulates records and flushes them in document order.
type Builder struct {
	w      *bufio.Writer
	fps    timeline.FPS
	closed bool
}

// NewBuilder wraps a writer; all output is buffered until Close.
func NewBuilder(w io.Writer, fps timeline.FPS) *Builder {
	return &Builder{w: bufio.NewWriter(w), fps: fps}
}

// AddCue writes one cue record. dropped marks cues removed by clipping.
func (b *Builder) AddCue(cr check.CueReport, dropped bool) error {
	startFrame, endFrame := b.fps.FrameRange(cr.Cue.Start, cr.Cue.End)
	rec := CueRecord{
		Type:         "cue",
		CueID:        cr.Cue.ID,
		StartFrame:   startFrame,
		EndFrame:     endFrame,
		DroppedCount: boolToInt(dropped),
	}
	if cr.Matched {
		delta := int64(cr.Delta / time.Millisecond)
		rec.RoundtripDeltaMS = &delta
	}
	if cr.Issue != "" {
		rec.Issue = cr.Issue
	}
	return b.write(rec)
}

// AddSummary writes the trailing summary line.
func (b *Builder) AddSummary(s SummaryRecord) error {
	s.Type = "summary"
	if err := b.write(s); err != nil {
		return err
	}
	return b.w.Flush()
}

func (b *Builder) write(v any) error {
	line, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := b.w.Write(append(line, '\n')); err != nil {
		return err
	}
	return nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
