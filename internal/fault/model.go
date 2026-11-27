// Package fault defines the single error model shared by the DASH and HLS
// parsers, segment scan, fingerprint index, checkpoint store and recovery
// planner. Every issue keeps the source manifest, representation, segment and
// an input locator (file, line or byte offset) so the anomaly report can always
// be traced back to the input byte that produced it.
package fault

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"mediabatch/internal/clock"
)

// Code is a stable machine-readable anomaly code.
type Code string

const (
	CodeParse             Code = "E_PARSE"
	CodeMissingDuration   Code = "E_HLS_MISSING_DURATION"
	CodeUnknownTag        Code = "E_HLS_UNKNOWN_TAG"
	CodeDuplicateRef      Code = "E_HLS_DUPLICATE_REF"
	CodeOutOfRangeRef     Code = "E_HLS_OUT_OF_RANGE_REF"
	CodeDiscontinuity     Code = "E_HLS_DISCONTINUITY_BOUNDARY"
	CodeDashNoDuration    Code = "E_DASH_MISSING_DURATION"
	CodeDashBadRef        Code = "E_DASH_BAD_REF"
	CodeDashDiscontinuity Code = "E_DASH_DISCONTINUITY"
	CodeSegmentTruncated  Code = "E_SEG_TRUNCATED"
	CodeSegmentHash       Code = "E_SEG_HASH"
	CodeSegmentEmpty      Code = "E_SEG_EMPTY"
	CodeSegmentMissing    Code = "E_SEG_MISSING"
	CodeFingerprintDrift  Code = "E_FP_RANGE_DRIFT"
	CodeArtifactMissing   Code = "E_STATE_ARTIFACT_MISSING"
	CodeIndexCommit       Code = "E_STATE_INDEX_COMMIT"
	CodeIndexCorrupt      Code = "E_STATE_INDEX_CORRUPT"
	CodeCheckpoint        Code = "E_STATE_CHECKPOINT"
	CodeInputMissing      Code = "E_INPUT_MISSING"
)

// Severity classifies an issue. Only SeverityError makes a batch fail; warnings
// are informational entries that still appear in the traceable report.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// Locator pins an issue to the exact input position. Line is 1-based and
// ByteOffset is 0-based; either may be zero when it does not apply (a DASH
// reference has an offset into the MPEG-DASH XML, an HLS line has a line and an
// offset).
type Locator struct {
	File    string `json:"file"`
	Line    int    `json:"line,omitempty"`
	Offset  int64  `json:"offset"`
	Context string `json:"context,omitempty"`
}

// String renders the locator as file:line or file@offset.
func (l Locator) String() string {
	if l.Line > 0 {
		return fmt.Sprintf("%s:%d", l.File, l.Line)
	}
	return fmt.Sprintf("%s@%d", l.File, l.Offset)
}

// Source identifies which manifest/representation/segment an issue belongs to.
type Source struct {
	Manifest       string     `json:"manifest"`
	Representation string     `json:"representation,omitempty"`
	Segment        string     `json:"segment,omitempty"`
	Loc            Locator    `json:"locator"`
	Start          clock.Time `json:"start"`
	End            clock.Time `json:"end"`
}

// Action records what the run did about the issue.
type Action string

const (
	ActionIsolated Action = "isolated"  // bad item quarantined, batch continues
	ActionRecorded Action = "recorded"  // noted only, no media change
	ActionReused   Action = "reused"    // existing artifact reused
	ActionRepaired Action = "recovered" // recomputed from the affected chunk
	ActionSkipped  Action = "skipped"   // not published
)

// Issue is one traceable anomaly.
type Issue struct {
	Code     Code     `json:"code"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	Action   Action   `json:"action"`
	Source   Source   `json:"source"`
}

// List is a stable, sortable collection of issues.
type List []Issue

// Add appends an issue.
func (l *List) Add(i Issue) { *l = append(*l, i) }

// Errors returns the number of error-severity issues.
func (l List) Errors() int {
	n := 0
	for _, i := range l {
		if i.Severity == SeverityError {
			n++
		}
	}
	return n
}

// Sort orders issues by start time, then manifest, representation, segment and
// locator so reports are deterministic across runs.
func (l List) Sort() {
	sort.SliceStable(l, func(a, b int) bool {
		x, y := l[a].Source, l[b].Source
		if c := x.Start.Cmp(y.Start); c != 0 {
			return c < 0
		}
		if x.Manifest != y.Manifest {
			return x.Manifest < y.Manifest
		}
		if x.Representation != y.Representation {
			return x.Representation < y.Representation
		}
		if x.Segment != y.Segment {
			return x.Segment < y.Segment
		}
		if x.Loc.File != y.Loc.File {
			return x.Loc.File < y.Loc.File
		}
		if x.Loc.Line != y.Loc.Line {
			return x.Loc.Line < y.Loc.Line
		}
		if x.Loc.Offset != y.Loc.Offset {
			return x.Loc.Offset < y.Loc.Offset
		}
		return l[a].Code < l[b].Code
	})
}

// MissingSource counts issues whose source locator is incomplete. The verify
// command requires this to be zero.
func (l List) MissingSource() int {
	n := 0
	for _, i := range l {
		if i.Source.Manifest == "" || i.Source.Loc.File == "" {
			n++
		}
	}
	return n
}

// SortedViolations counts entries that are out of canonical order. The verify
// command requires this to be zero.
func (l List) SortedViolations() int {
	n := 0
	for k := 1; k < len(l); k++ {
		a, b := l[k-1].Source, l[k].Source
		if a.Start.Cmp(b.Start) > 0 {
			n++
		}
	}
	return n
}

// Canonical serialises a list deterministically for hashing.
func (l List) Canonical() string {
	var b strings.Builder
	for _, i := range l {
		fmt.Fprintf(&b, "%s|%s|%s|%s|%s|%s|%d|%d|%s|%s|%s|%s\n",
			i.Code, i.Severity, i.Action, i.Source.Manifest,
			i.Source.Representation, i.Source.Segment,
			i.Source.Loc.Line, i.Source.Loc.Offset,
			i.Source.Loc.File, i.Source.Start, i.Source.End, i.Message)
	}
	return b.String()
}

// Digest returns the SHA-256 of the canonical list; identical runs with
// identical input produce the same digest.
func (l List) Digest() string {
	sum := sha256.Sum256([]byte(l.Canonical()))
	return hex.EncodeToString(sum[:])
}

// Errorf builds an error issue.
func Errorf(code Code, sev Severity, action Action, src Source, format string, args ...interface{}) Issue {
	return Issue{Code: code, Severity: sev, Action: action, Source: src, Message: fmt.Sprintf(format, args...)}
}

// Err carries a Code and a Locator as a Go error, used at package boundaries.
type Err struct {
	Code Code
	Loc  Locator
	Msg  string
}

func (e *Err) Error() string {
	if e.Loc.File != "" {
		return fmt.Sprintf("%s: %s (%s)", e.Code, e.Msg, e.Loc)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Msg)
}

// NewErr constructs an *Err.
func NewErr(code Code, loc Locator, format string, args ...interface{}) *Err {
	return &Err{Code: code, Loc: loc, Msg: fmt.Sprintf(format, args...)}
}
