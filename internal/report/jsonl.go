// Package report renders the stable run report: a summary, per-stage
// errors with the first byte offset involved, clock-repair events and the
// per-position mapping items. Output is fully deterministic: entries are
// sorted and no wall-clock or host paths are included.
package report

import (
	"encoding/json"
	"io"
	"sort"

	"mediarc/internal/merge"
	"mediarc/internal/model"
)

// Summary is the stable run summary.
type Summary struct {
	Mapped      int   `json:"mapped"`
	Missing     int   `json:"missing"`
	Conflicts   int   `json:"conflicts"`
	Rollback    int   `json:"rollback"`
	FirstError  int64 `json:"first_error"` // -1 when no errors
	PeakBytes   int64 `json:"peak_bytes"`
	MemoryLimit int64 `json:"memory_limit"`
}

// ErrorEntry is one stage-tagged error.
type ErrorEntry struct {
	Stage  string     `json:"stage"`
	Code   model.Code `json:"code"`
	Track  int        `json:"track"`
	Offset int64      `json:"offset"`
	Msg    string     `json:"msg"`
}

// Event is one clock-repair event (wraparound or source switch).
type Event struct {
	Track  int        `json:"track"`
	Offset int64      `json:"offset"`
	Kind   model.Code `json:"kind"`
	Raw    uint32     `json:"raw"`
	Delta  int64      `json:"delta"`
}

// Report is the full run report.
type Report struct {
	Summary Summary      `json:"summary"`
	Errors  []ErrorEntry `json:"errors"`
	Events  []Event      `json:"events"`
	Items   []merge.Item `json:"items"`
}

// Finalize sorts all sections and derives the summary's first error offset.
func (r *Report) Finalize() {
	sort.Slice(r.Errors, func(i, j int) bool {
		if r.Errors[i].Offset != r.Errors[j].Offset {
			return r.Errors[i].Offset < r.Errors[j].Offset
		}
		return r.Errors[i].Code < r.Errors[j].Code
	})
	sort.Slice(r.Events, func(i, j int) bool {
		if r.Events[i].Track != r.Events[j].Track {
			return r.Events[i].Track < r.Events[j].Track
		}
		return r.Events[i].Offset < r.Events[j].Offset
	})
	r.Summary.FirstError = -1
	if len(r.Errors) > 0 {
		r.Summary.FirstError = r.Errors[0].Offset
	}
}

// Write renders the report as deterministic indented JSON.
func Write(w io.Writer, r *Report) error {
	if r.Errors == nil {
		r.Errors = []ErrorEntry{}
	}
	if r.Events == nil {
		r.Events = []Event{}
	}
	if r.Items == nil {
		r.Items = []merge.Item{}
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	_, err = w.Write(append(data, '\n'))
	return err
}
