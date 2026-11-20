package report

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
)

// Format selects the output encoding.
type Format string

const (
	FormatTable Format = "table"
	FormatJSON  Format = "json"
)

// ParseFormat validates a --format value.
func ParseFormat(s string) (Format, error) {
	switch Format(strings.ToLower(strings.TrimSpace(s))) {
	case FormatTable, "":
		return FormatTable, nil
	case FormatJSON:
		return FormatJSON, nil
	default:
		return "", fmt.Errorf("unknown format %q (want table or json)", s)
	}
}

// Status is the verdict for one file.
type Status string

const (
	StatusPass          Status = "pass"
	StatusLoudnessFail  Status = "fail_loudness"
	StatusPeakFail      Status = "fail_true_peak"
	StatusBothFail      Status = "fail_loudness_and_peak"
	StatusNoMeasurement Status = "no_measurement"
	StatusError         Status = "error"
)

// Level is a loudness or peak value in dB. Without one it renders as "-inf",
// both in the table and in JSON: encoding/json cannot carry infinities, and a
// string is unambiguous where null would be mistaken for "field missing".
// NaN never reaches here; the measurement code reports -Inf instead.
type Level struct {
	Value float64
	Valid bool
}

// MeasuredLevel builds a valid Level.
func MeasuredLevel(v float64) Level {
	if math.IsNaN(v) {
		v = math.Inf(-1)
	}
	return Level{Value: v, Valid: !math.IsInf(v, -1)}
}

// NoLevel is the unmeasurable case.
func NoLevel() Level { return Level{} }

// String renders the value with the given precision.
func (l Level) String(digits int) string {
	if !l.Valid || math.IsInf(l.Value, -1) {
		return "-inf"
	}
	return strconv.FormatFloat(l.Value, 'f', digits, 64)
}

// MarshalJSON emits a number, or "-inf" for unmeasurable material.
func (l Level) MarshalJSON() ([]byte, error) {
	if !l.Valid || math.IsInf(l.Value, -1) {
		return []byte(`"-inf"`), nil
	}
	if math.IsNaN(l.Value) {
		return nil, fmt.Errorf("refusing to marshal NaN level")
	}
	return []byte(strconv.FormatFloat(l.Value, 'f', -1, 64)), nil
}

// Entry is the stable, sortable record for one input file.
type Entry struct {
	Path             string  `json:"path"`
	SampleRate       int     `json:"sample_rate"`
	Channels         int     `json:"channels"`
	ChannelLayout    string  `json:"channel_layout"`
	IntegratedLUFS   Level   `json:"integrated_lufs"`
	MaxShortTermLUFS Level   `json:"max_short_term_lufs"`
	TruePeakDBTP     Level   `json:"true_peak_dbtp"`
	LoudnessLimit    float64 `json:"loudness_limit"`
	PeakLimit        float64 `json:"peak_limit"`
	Status           Status  `json:"status"`
	// ErrorStage and Error are always present so every file object has the
	// same key set; they are empty for files that measured successfully.
	ErrorStage string `json:"error_stage"`
	Error      string `json:"error"`
}

// Report is the top-level JSON document.
type Report struct {
	Entries []Entry `json:"files"`
	Summary Summary `json:"summary"`
}

// Summary is the batch verdict; ExitCode is the process exit status the CLI
// returns.
type Summary struct {
	Files      int `json:"files"`
	Passed     int `json:"passed"`
	Failed     int `json:"failed"`
	Unmeasured int `json:"unmeasured"`
	Errors     int `json:"errors"`
	ExitCode   int `json:"exit_code"`
}

// Build assembles a Report from entries, sorting them by path so output is
// stable regardless of concurrency.
func Build(entries []Entry) Report {
	sorted := make([]Entry, len(entries))
	copy(sorted, entries)
	sortEntries(sorted)
	sum := Summary{Files: len(sorted)}
	for _, e := range sorted {
		switch e.Status {
		case StatusError:
			sum.Errors++
		case StatusNoMeasurement:
			sum.Unmeasured++
		case StatusPass:
			sum.Passed++
		default:
			sum.Failed++
		}
	}
	switch {
	case sum.Errors > 0:
		sum.ExitCode = 3
	case sum.Failed > 0 || sum.Unmeasured > 0:
		sum.ExitCode = 1
	}
	return Report{Entries: sorted, Summary: sum}
}

// Write renders the report in the requested format.
func Write(w io.Writer, r Report, format Format) error {
	if format == FormatJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	return writeTable(w, r)
}

func writeTable(w io.Writer, r Report) error {
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "file\tsample_rate\tchannels\tchannel_layout\tintegrated_lufs\tmax_short_term_lufs\ttrue_peak_dbtp\tloudness_limit\tpeak_limit\tstatus\terror")
	for _, e := range r.Entries {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			e.Path,
			formatRate(e.SampleRate),
			e.Channels,
			orDash(e.ChannelLayout),
			e.IntegratedLUFS.String(1),
			e.MaxShortTermLUFS.String(1),
			e.TruePeakDBTP.String(2),
			strconv.FormatFloat(e.LoudnessLimit, 'f', 1, 64),
			strconv.FormatFloat(e.PeakLimit, 'f', 1, 64),
			e.Status,
			formatError(e),
		)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(w, "\nfiles=%d passed=%d failed=%d unmeasured=%d errors=%d exit=%d\n",
		r.Summary.Files, r.Summary.Passed, r.Summary.Failed,
		r.Summary.Unmeasured, r.Summary.Errors, r.Summary.ExitCode)
	return nil
}

func formatRate(rate int) string {
	if rate <= 0 {
		return "-"
	}
	return strconv.Itoa(rate)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func formatError(e Entry) string {
	if e.Error == "" {
		return "-"
	}
	// Typed errors already begin with their stage name; do not repeat it.
	if e.ErrorStage != "" && !strings.HasPrefix(e.Error, e.ErrorStage+": ") {
		return e.ErrorStage + ": " + e.Error
	}
	return e.Error
}

// Stdout is the writer the CLI uses.
var Stdout io.Writer = os.Stdout

// Reported says whether the entry carries a verdict. An Entry with no status
// was never processed (for example when the run was cancelled) and must not
// be treated as a pass.
func (e Entry) Reported() bool { return e.Status != "" }
