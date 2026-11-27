// Package scan validates individual media segments. ISO BMFF fragments are read
// with go-mp4 and RTP packet dumps with pion/rtp. It reports truncated
// segments, hash mismatches and discontinuity boundary problems, and never
// rewrites or fabricates media data: a bad segment is isolated, not repaired.
package scan

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	"mediabatch/internal/clock"
	"mediabatch/internal/fault"
)

// Status is the validation outcome for one segment.
type Status string

const (
	StatusOK           Status = "ok"
	StatusMissing      Status = "missing"
	StatusEmpty        Status = "empty"
	StatusTruncated    Status = "truncated"
	StatusHashMismatch Status = "hash-mismatch"
	StatusMalformed    Status = "malformed"
	StatusOversize     Status = "oversize"
)

// DefaultMaxSegmentBytes bounds a single segment read.
const DefaultMaxSegmentBytes int64 = 64 << 20

// Expectation is an integrity record produced by the fixture generator.
type Expectation struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Request describes one segment to validate.
type Request struct {
	Manifest       string
	Representation string
	Ref            string
	Path           string
	InitPath       string
	Start          clock.Time
	End            clock.Time
	Discontinuity  bool
	Expected       *Expectation
	MaxBytes       int64
}

// MediaInfo summarises the parsed container.
type MediaInfo struct {
	Kind         string     `json:"kind"`
	SampleCount  int        `json:"sampleCount"`
	TrackID      uint32     `json:"trackId"`
	Timescale    uint32     `json:"timescale"`
	BaseDecode   clock.Time `json:"baseDecode"`
	Keyframe     bool       `json:"keyframe"`
	PayloadBytes int64      `json:"payloadBytes"`
}

// Result is the validation result for one segment.
type Result struct {
	Request Request
	Status  Status
	Hash    string
	Size    int64
	Info    MediaInfo
	Issues  fault.List
}

// OK reports whether the segment may be published.
func (r Result) OK() bool { return r.Status == StatusOK }

// Err is returned when a segment cannot be read at all.
type Err struct {
	Status Status
	Err    error
}

func (e *Err) Error() string { return string(e.Status) + ": " + e.Err.Error() }

func (r *Result) fail(code fault.Code, msg string, offset int64) {
	r.Issues = append(r.Issues, fault.Issue{
		Code: code, Severity: fault.SeverityError, Action: fault.ActionIsolated,
		Message: msg,
		Source: fault.Source{
			Manifest: r.Request.Manifest, Representation: r.Request.Representation,
			Segment: r.Request.Ref,
			Loc:     fault.Locator{File: r.Request.Path, Offset: offset},
			Start:   r.Request.Start, End: r.Request.End,
		},
	})
}

var errTooLarge = fmt.Errorf("input exceeds configured limit")

// Scan validates one segment file against optional generator expectations.
func Scan(req Request) Result {
	res := Result{Request: req}
	max := req.MaxBytes
	if max <= 0 {
		max = DefaultMaxSegmentBytes
	}
	data, err := readLimited(req.Path, max)
	if err != nil {
		switch {
		case os.IsNotExist(err):
			res.Status = StatusMissing
			res.fail(fault.CodeSegmentMissing, "segment file does not exist", 0)
		case err == errTooLarge:
			res.Status = StatusOversize
			res.fail(fault.CodeInputMissing, fmt.Sprintf("segment exceeds the %d byte single-batch limit", max), 0)
		default:
			res.Status = StatusMalformed
			res.fail(fault.CodeInputMissing, fmt.Sprintf("read segment: %v", err), 0)
		}
		res.Issues.Sort()
		return res
	}
	res.Size = int64(len(data))
	if len(data) == 0 {
		res.Status = StatusEmpty
		res.fail(fault.CodeSegmentEmpty, "segment is empty", 0)
		res.Issues.Sort()
		return res
	}
	sum := sha256.Sum256(data)
	res.Hash = hex.EncodeToString(sum[:])
	if exp := req.Expected; exp != nil {
		if exp.Size > 0 && exp.Size != res.Size {
			res.Status = StatusTruncated
			res.fail(fault.CodeSegmentTruncated,
				fmt.Sprintf("size %d does not match recorded size %d", res.Size, exp.Size), 0)
		}
		if exp.SHA256 != "" && !strings.EqualFold(exp.SHA256, res.Hash) {
			res.Status = StatusHashMismatch
			res.fail(fault.CodeSegmentHash, "content hash does not match the recorded digest", 0)
		}
	}

	kind, info, issues := inspect(req, data)
	res.Info = info
	res.Info.Kind = kind
	res.Issues = append(res.Issues, issues...)
	res.Issues.Sort()
	if res.Status == "" {
		res.Status = StatusOK
		for _, i := range res.Issues {
			if i.Severity == fault.SeverityError {
				res.Status = StatusMalformed
				break
			}
		}
	}
	return res
}

// ScanAll validates a batch of requests, preserving input order.
func ScanAll(reqs []Request) []Result {
	out := make([]Result, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, Scan(r))
	}
	return out
}

func readLimited(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > max {
		return nil, errTooLarge
	}
	return io.ReadAll(io.LimitReader(f, max+1))
}

func isBoxHeader(b []byte) bool {
	if len(b) < 8 {
		return false
	}
	switch string(b[4:8]) {
	case "ftyp", "styp", "moof", "moov", "mdat", "sidx", "free", "skip":
		return true
	}
	return false
}

// inspect dispatches on the file signature.
func inspect(req Request, data []byte) (string, MediaInfo, fault.List) {
	if isBoxHeader(data) {
		return inspectMP4(req, data)
	}
	if IsRTPDump(data) {
		return inspectRTP(req, data)
	}
	var l fault.List
	l = append(l, fault.Issue{
		Code: fault.CodeSegmentEmpty, Severity: fault.SeverityError, Action: fault.ActionIsolated,
		Message: "unrecognised segment container",
		Source: fault.Source{
			Manifest: req.Manifest, Representation: req.Representation, Segment: req.Ref,
			Loc:   fault.Locator{File: req.Path, Offset: 0},
			Start: req.Start, End: req.End,
		},
	})
	return "unknown", MediaInfo{}, l
}

func boundaryIssue(req Request, sev fault.Severity, code fault.Code, msg string) fault.Issue {
	return fault.Issue{
		Code: code, Severity: sev, Action: fault.ActionIsolated,
		Message: msg,
		Source: fault.Source{
			Manifest: req.Manifest, Representation: req.Representation, Segment: req.Ref,
			Loc:   fault.Locator{File: req.Path, Offset: 0},
			Start: req.Start, End: req.End,
		},
	}
}
