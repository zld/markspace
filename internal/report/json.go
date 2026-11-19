// Package report renders the deterministic validation summary. Field
// order is fixed by the struct so JSON output is byte-stable across
// runs and transport modes.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Report is the full validation result. Field order below defines the
// JSON key order; do not reorder without updating the acceptance docs.
type Report struct {
	Mode              string `json:"mode"`
	StateTransitions  int    `json:"state_transitions"`
	IllegalRejections int    `json:"illegal_rejections"`
	ReceivedPackets   int    `json:"received_packets"`
	SequenceGaps      int    `json:"sequence_gaps"`
	DuplicatePackets  int    `json:"duplicate_packets"`
	LatePackets       int    `json:"late_packets"`
	MalformedPackets  int    `json:"malformed_packets"`
	CompleteFrames    int    `json:"complete_frames"`
	IncompleteFrames  int    `json:"incomplete_frames"`
	EvictedFrames     int    `json:"evicted_frames"`
	FinalState        string `json:"final_state"`
	Seed              int64  `json:"seed"`
	FramesRequested   int    `json:"frames_requested"`
}

// Format selects the rendering style.
type Format string

const (
	// FormatJSON emits one pretty-printed JSON object.
	FormatJSON Format = "json"
	// FormatSummary emits one stable single-line key=value record.
	FormatSummary Format = "summary"
)

// ParseFormat validates a format flag.
func ParseFormat(s string) (Format, error) {
	switch Format(s) {
	case FormatJSON, FormatSummary:
		return Format(s), nil
	default:
		return "", fmt.Errorf("report: format must be %q or %q", FormatJSON, FormatSummary)
	}
}

// Write renders r in the requested format to w.
func (r Report) Write(w io.Writer, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(r); err != nil {
			return fmt.Errorf("report: json encode: %w", err)
		}
		return nil
	case FormatSummary:
		_, err := fmt.Fprintln(w, r.summarize())
		return err
	default:
		return fmt.Errorf("report: unsupported format %q", f)
	}
}

// summarize returns a deterministic single-line record with the same
// fields as the JSON output, in the same order.
func (r Report) summarize() string {
	fields := []string{
		"mode=" + r.Mode,
		kvInt("state_transitions", r.StateTransitions),
		kvInt("illegal_rejections", r.IllegalRejections),
		kvInt("received_packets", r.ReceivedPackets),
		kvInt("sequence_gaps", r.SequenceGaps),
		kvInt("duplicate_packets", r.DuplicatePackets),
		kvInt("late_packets", r.LatePackets),
		kvInt("malformed_packets", r.MalformedPackets),
		kvInt("complete_frames", r.CompleteFrames),
		kvInt("incomplete_frames", r.IncompleteFrames),
		kvInt("evicted_frames", r.EvictedFrames),
		"final_state=" + r.FinalState,
		kvInt64("seed", r.Seed),
		kvInt("frames_requested", r.FramesRequested),
	}
	return strings.Join(fields, " ")
}

func kvInt(key string, v int) string { return key + "=" + strconv.Itoa(v) }
func kvInt64(key string, v int64) string {
	return key + "=" + strconv.FormatInt(v, 10)
}
