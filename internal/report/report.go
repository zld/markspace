// Package report renders the traceable anomaly list and the run summaries.
// Every entry carries its error code, source locator and recovery action, and
// the list is sorted by media start time. The final digest is stable across
// repeated runs over identical inputs.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"mediabatch/internal/clock"
	"mediabatch/internal/fault"
)

// Entry is one line of the anomaly report.
type Entry struct {
	Code           fault.Code     `json:"code"`
	Severity       fault.Severity `json:"severity"`
	Action         fault.Action   `json:"action"`
	Manifest       string         `json:"manifest"`
	Representation string         `json:"representation,omitempty"`
	Segment        string         `json:"segment,omitempty"`
	File           string         `json:"file"`
	Line           int            `json:"line,omitempty"`
	Offset         int64          `json:"offset"`
	Start          clock.Time     `json:"start"`
	End            clock.Time     `json:"end"`
	Message        string         `json:"message"`
	// Recovery is the recovery action taken for this entry, when applicable.
	Recovery string `json:"recovery,omitempty"`
	// Verified is the final validation summary for the affected chunk.
	Verified string `json:"verified,omitempty"`
}

// Report is the full traceable anomaly document.
type Report struct {
	Entries []Entry `json:"entries"`
	// Digest is the SHA-256 of the canonical entries.
	Digest string `json:"digest"`
	// Counts summarises the batch.
	Counts Counts `json:"counts"`
}

// Counts are the summary numbers printed by demo, resume and verify.
type Counts struct {
	Segments     int `json:"segments"`
	Anomalies    int `json:"anomalies"`
	Duplicates   int `json:"duplicates"`
	Chunks       int `json:"chunks"`
	Published    int `json:"published"`
	Reused       int `json:"reused"`
	Recomputed   int `json:"recomputed"`
	Revalidated  int `json:"revalidated"`
	NewArtifacts int `json:"newArtifacts"`
}

// Build converts a fault list and optional recovery annotations into a report.
func Build(issues fault.List, counts Counts, recovery map[string]string) *Report {
	r := &Report{Counts: counts}
	for _, i := range issues {
		e := Entry{
			Code: i.Code, Severity: i.Severity, Action: i.Action,
			Manifest: i.Source.Manifest, Representation: i.Source.Representation,
			Segment: i.Source.Segment, File: i.Source.Loc.File,
			Line: i.Source.Loc.Line, Offset: i.Source.Loc.Offset,
			Start: i.Source.Start, End: i.Source.End, Message: i.Message,
		}
		if recovery != nil {
			e.Recovery = recovery[string(i.Code)]
		}
		r.Entries = append(r.Entries, e)
	}
	r.Sort()
	r.Digest = r.canonicalDigest()
	return r
}

// Sort orders entries by start time, then source position and code.
func (r *Report) Sort() {
	sort.SliceStable(r.Entries, func(i, j int) bool {
		a, b := r.Entries[i], r.Entries[j]
		if c := a.Start.Cmp(b.Start); c != 0 {
			return c < 0
		}
		if a.Manifest != b.Manifest {
			return a.Manifest < b.Manifest
		}
		if a.Representation != b.Representation {
			return a.Representation < b.Representation
		}
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Offset != b.Offset {
			return a.Offset < b.Offset
		}
		return a.Code < b.Code
	})
}

func (r *Report) canonicalDigest() string {
	l := make(fault.List, 0, len(r.Entries))
	for _, e := range r.Entries {
		l = append(l, fault.Issue{
			Code: e.Code, Severity: e.Severity, Action: e.Action,
			Message: e.Message,
			Source: fault.Source{
				Manifest: e.Manifest, Representation: e.Representation, Segment: e.Segment,
				Loc:   fault.Locator{File: e.File, Line: e.Line, Offset: e.Offset},
				Start: e.Start, End: e.End,
			},
		})
	}
	return l.Digest()
}

// SortedViolations counts entries out of start-time order.
func (r *Report) SortedViolations() int {
	n := 0
	for i := 1; i < len(r.Entries); i++ {
		if r.Entries[i-1].Start.Cmp(r.Entries[i].Start) > 0 {
			n++
		}
	}
	return n
}

// MissingSource counts entries without a manifest file and a locator file.
func (r *Report) MissingSource() int {
	n := 0
	for _, e := range r.Entries {
		if e.Manifest == "" || e.File == "" {
			n++
		}
	}
	return n
}

// WriteJSON writes the report as indented JSON.
func (r *Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteText writes the human-readable report used by the CLI.
func (r *Report) WriteText(w io.Writer) error {
	fmt.Fprintf(w, "segments=%d anomalies=%d duplicates=%d chunks=%d\n",
		r.Counts.Segments, r.Counts.Anomalies, r.Counts.Duplicates, r.Counts.Chunks)
	for _, e := range r.Entries {
		loc := e.File
		if e.Line > 0 {
			loc = fmt.Sprintf("%s:%d", e.File, e.Line)
		} else if e.Offset > 0 {
			loc = fmt.Sprintf("%s@%d", e.File, e.Offset)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%-28s %-7s %-10s %s [%s..%s]",
			e.Code, e.Severity, e.Action, loc, e.Start, e.End)
		if e.Segment != "" {
			fmt.Fprintf(&b, " seg=%s", e.Segment)
		}
		if e.Recovery != "" {
			fmt.Fprintf(&b, " recovery=%s", e.Recovery)
		}
		fmt.Fprintf(&b, " :: %s\n", e.Message)
		if _, err := io.WriteString(w, b.String()); err != nil {
			return err
		}
	}
	fmt.Fprintf(w, "report-digest=%s\n", r.Digest)
	return nil
}

// Verify is the machine-checkable verification summary.
type Verify struct {
	Entries          int    `json:"entries"`
	SortedViolations int    `json:"sortedViolations"`
	MissingSource    int    `json:"missingSource"`
	Digest           string `json:"digest"`
}

// Summarise builds the verification view of a report.
func (r *Report) Summarise() Verify {
	return Verify{
		Entries:          len(r.Entries),
		SortedViolations: r.SortedViolations(),
		MissingSource:    r.MissingSource(),
		Digest:           r.Digest,
	}
}
