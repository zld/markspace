// Package pipeline wires every stage together: decode, parse, overlap
// resolution, serialization, round-trip verification and reporting.
package pipeline

import (
	"fmt"
	"os"
	"time"

	"subsync/internal/atomicio"
	"subsync/internal/check"
	"subsync/internal/cli"
	"subsync/internal/decode"
	"subsync/internal/model"
	"subsync/internal/overlap"
	"subsync/internal/parse"
	"subsync/internal/report"
	"subsync/internal/serialize"
)

// StageError classifies failures by pipeline stage for exit-code mapping.
type StageError struct {
	Stage string
	Err   error
}

func (e *StageError) Error() string { return e.Stage + ": " + e.Err.Error() }
func (e *StageError) Unwrap() error { return e.Err }

// Run executes the full conversion for raw input bytes and returns the
// rendered output text alongside verification results and counters.
func Run(opts *cli.Options, raw []byte, encodingLabel string) (
	outputText string, rep *check.Report, summary report.SummaryRecord, outCues []model.Cue, err error,
) {
	mode := model.ParseFail
	if opts.SkipBad {
		mode = model.ParseSkip
	}

	text, label, err := decode.Sniff(raw, opts.Encoding)
	if err != nil {
		return "", nil, summary, nil, &StageError{Stage: "decode", Err: err}
	}
	if encodingLabel == "" {
		encodingLabel = label
	}

	parser, err := parse.ForFormat(opts.InputFormat)
	if err != nil {
		return "", nil, summary, nil, &StageError{Stage: "parse", Err: err}
	}
	doc, err := parser.Parse(text, opts.Track, mode)
	if err != nil {
		return "", nil, summary, nil, &StageError{Stage: "parse", Err: err}
	}
	if len(doc.Cues) > atomicio.MaxCues {
		return "", nil, summary, nil, &StageError{
			Stage: "parse",
			Err:   fmt.Errorf("cue count %d exceeds per-file limit %d", len(doc.Cues), atomicio.MaxCues),
		}
	}
	inputCount := len(doc.Cues)

	res, err := overlap.Resolve(doc.Cues, opts.Overlap)
	if err != nil {
		return "", nil, summary, nil, &StageError{Stage: "overlap", Err: err}
	}
	doc.Cues = res.Cues

	serialize.AccountStyleLoss(doc, opts.OutputFormat)
	rendered, err := serialize.Write(doc, opts.OutputFormat)
	if err != nil {
		return "", nil, summary, nil, &StageError{Stage: "serialize", Err: err}
	}

	// Round-trip: feed the rendered text back through the target parser.
	reParser, err := parse.ForFormat(opts.OutputFormat)
	if err != nil {
		return "", nil, summary, nil, &StageError{Stage: "roundtrip", Err: err}
	}
	reDoc, err := reParser.Parse(rendered, opts.Track, model.ParseFail)
	if err != nil {
		return "", nil, summary, nil, &StageError{Stage: "roundtrip", Err: err}
	}

	rep = check.Verify(doc.Cues, reDoc.Cues)

	summary = report.SummaryRecord{
		InputCount:   inputCount,
		OutputCount:  len(reDoc.Cues),
		LostCount:    res.Dropped + rep.Missing,
		OverlapCount: res.Overlaps,
		StyleLoss:    doc.StyleLoss,
		Encoding:     encodingLabel,
		MaxDeltaMS:   int64(rep.MaxDelta / time.Millisecond),
	}
	return rendered, rep, summary, reDoc.Cues, nil
}

// WriteOutput persists rendered text with the atomic temp-file contract.
func WriteOutput(path string, data []byte) error {
	if err := atomicio.WriteFile(path, data, 0o644); err != nil {
		return &StageError{Stage: "io", Err: err}
	}
	return nil
}

// EmitReport writes per-cue and summary JSONL records.
func EmitReport(w *os.File, opts *cli.Options, rep *check.Report, droppedIDs map[string]bool, summary report.SummaryRecord) error {
	b := report.NewBuilder(w, opts.FPS)
	for _, cr := range rep.PerCue {
		if err := b.AddCue(cr, droppedIDs[cr.Cue.ID]); err != nil {
			return &StageError{Stage: "report", Err: err}
		}
	}
	if err := b.AddSummary(summary); err != nil {
		return &StageError{Stage: "report", Err: err}
	}
	return nil
}

// ThresholdExceeded applies the user-supplied deviation and drop limits.
// LostCount already includes every missing cue, so it alone represents the
// drop budget and is never double-counted.
func ThresholdExceeded(rep *check.Report, summary report.SummaryRecord, opts *cli.Options) bool {
	if summary.MaxDeltaMS > opts.MaxDeltaMS {
		return true
	}
	return summary.LostCount > opts.MaxDropped
}
