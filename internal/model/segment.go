// Package model defines the data carried between scanning, manifest
// building/parsing, verification and reporting.
package model

import "fmt"

// MaxSegments is the hard upper bound on how many segments a single rate
// may declare. It keeps verification work bounded.
const MaxSegments = 100000

// Segment is one media object on disk, identified by its rate and sequence
// number. DurationMS is the millisecond duration claimed for the object.
type Segment struct {
	Rate       int    `json:"rate"`
	Number     int    `json:"number"`
	DurationMS int64  `json:"duration_ms"`
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
}

// Rate is one bitrate variant: all segments that share the same bitrate.
// Segments are ordered by number once scanned.
type Rate struct {
	Rate     int       `json:"rate"`
	Segments []Segment `json:"segments"`
}

// Count returns the number of segments in the rate.
func (r Rate) Count() int { return len(r.Segments) }

// DurationMS returns the summed declared duration of the rate's segments.
func (r Rate) DurationMS() int64 {
	var total int64
	for _, s := range r.Segments {
		total += s.DurationMS
	}
	return total
}

// Manifest is the parsed form of a generated media playlist. It is produced
// only by the manifest parser so that generation and verification agree on
// normalization rules.
type Manifest struct {
	Rate             int             `json:"rate"`
	TargetDurationMS int64           `json:"target_duration_ms"`
	MediaSequence    int             `json:"media_sequence"`
	Segments         []ManifestEntry `json:"segments"`
	EndList          bool            `json:"end_list"`
}

// ManifestEntry is a single EXTINF duration plus its URI.
type ManifestEntry struct {
	DurationMS int64  `json:"duration_ms"`
	URI        string `json:"uri"`
}

// DurationMS returns the summed segment durations declared by the manifest.
func (m Manifest) DurationMS() int64 {
	var total int64
	for _, e := range m.Segments {
		total += e.DurationMS
	}
	return total
}

// IssueKind classifies a verification finding.
type IssueKind string

const (
	IssueMissing       IssueKind = "missing"
	IssueDuplicate     IssueKind = "duplicate"
	IssueOutOfOrder    IssueKind = "out_of_order"
	IssueDuration      IssueKind = "duration_mismatch"
	IssueCrossRate     IssueKind = "cross_rate"
	IssueChecksum      IssueKind = "checksum_failure"
	IssueManifest      IssueKind = "manifest"
	IssueUnparseable   IssueKind = "unparseable"
	IssueScanError     IssueKind = "scan_error"
	IssueWrite         IssueKind = "write_error"
	IssueFileOperation IssueKind = "file_operation"
)

// Issue is one problem found by the verifier.
type Issue struct {
	Kind    IssueKind `json:"kind"`
	Rate    int       `json:"rate"`
	Number  int       `json:"number"`
	Message string    `json:"message"`
}

func (i Issue) String() string {
	return fmt.Sprintf("%s rate=%d number=%d: %s", i.Kind, i.Rate, i.Number, i.Message)
}

// Status is the outcome of a verification run.
type Status string

const (
	StatusPass  Status = "pass"
	StatusFail  Status = "fail"
	StatusError Status = "error"
)

// Check is the exit code contract shared by the CLI and the report writer.
const (
	ExitPass  = 0
	ExitFail  = 1
	ExitError = 2
)
