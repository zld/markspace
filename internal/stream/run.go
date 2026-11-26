// Package stream executes a scan over a half-open interval [from,to). It reads
// only the segments the index says overlap the interval, verifies each
// fragment's hash against the index, and on damage skips ahead to the next
// segment whose keyframe verifies, reporting the unrecovered range and the
// original offsets. Nothing is re-encoded or fabricated.
package stream

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"medtrace/internal/core"
)

// Result is the outcome of one run.
type Result struct {
	// Processed lists, in manifest order, every sample the run emitted.
	Processed []core.Sample
	// Anomalies found during execution, ordered deterministically.
	Anomalies []core.Anomaly
	// Recoveries records each local recovery attempt.
	Recoveries []core.Recovery
	// HashChecks counts segments whose SHA-256 was compared to the index.
	HashChecks int
	// HashMatches counts those that agreed.
	HashMatches int
	// KeyframeHits counts starts that landed on a verified keyframe.
	KeyframeHits int
	// KeyframeTries counts starts attempted.
	KeyframeTries int
}

// Runner executes an index over an interval.
type Runner struct {
	Root  string
	Index *core.Index
	// VerifyHashes enables per-segment SHA-256 comparison against the index.
	VerifyHashes bool
}

// Run processes [from,to). It is deterministic: the same index, interval and
// files always yield the same Processed order, anomaly order and recovery
// boundaries.
func (r *Runner) Run(from, to core.Nanos) (*Result, error) {
	if to <= from {
		return nil, core.NewFault(core.KindUsage, core.Pos{Byte: -1}, "run",
			fmt.Sprintf("empty interval [%s,%s)", from, to))
	}
	if err := r.Index.Validate(); err != nil {
		return nil, core.WrapFault(err, core.KindManifest, core.Pos{Byte: -1}, "run", "invalid index")
	}
	res := &Result{}
	segs := r.Index.SegmentsIn(from, to)
	for i := 0; i < len(segs); {
		damage := r.processSegment(segs[i], from, to, res)
		if damage == nil {
			i++
			continue
		}
		rec, resumeIdx := r.recover(segs, i, segs[i], damage, from, to, res)
		res.Recoveries = append(res.Recoveries, rec)
		if !rec.Resumed {
			i = len(segs)
			break
		}
		i = resumeIdx + 1
	}
	core.SortAnomalies(res.Anomalies)
	return res, nil
}

// processSegment verifies and emits one segment. It returns a non-nil fault
// when the segment cannot be trusted, carrying the original byte offset of the
// damage. The caller does not emit any sample from a failed segment.
func (r *Runner) processSegment(seg *core.Segment, from, to core.Nanos, res *Result) *core.Fault {
	if seg.Truncated {
		return &core.Fault{
			Kind: core.KindSegmentTruncated, Pos: core.Pos{File: seg.URI, Byte: firstBadOffset(seg)},
			Track: seg.Track, Detail: "segment is truncated; refusing to emit unverified samples",
		}
	}
	if r.VerifyHashes {
		sum, err := hashFile(r.Root, seg.URI)
		if err != nil {
			return core.WrapFault(err, core.KindIO, core.Pos{File: seg.URI, Byte: -1}, "verify", "hash segment")
		}
		res.HashChecks++
		if sum != seg.SHA256 {
			res.Anomalies = append(res.Anomalies, core.Anomaly{
				Category: core.CatHashMismatch, Pos: core.Pos{File: seg.URI, Byte: 0},
				Track: seg.Track, Segment: seg.URI, ByteOffset: 0,
				Expected: seg.SHA256, Actual: sum,
				Detail: "segment SHA-256 differs from the index",
			})
			return &core.Fault{Kind: core.KindHashMismatch, Pos: core.Pos{File: seg.URI, Byte: 0},
				Track: seg.Track, Detail: "hash mismatch"}
		}
		res.HashMatches++
	}
	for _, s := range seg.Samples {
		if s.Canonical < from || s.Canonical >= to {
			continue
		}
		res.Processed = append(res.Processed, s)
	}
	return nil
}

// firstBadOffset returns the original offset just past the last readable
// sample; it is where truncation was detected during the scan.
func firstBadOffset(seg *core.Segment) int64 {
	if len(seg.Samples) == 0 {
		return 0
	}
	last := seg.Samples[len(seg.Samples)-1]
	return last.ByteOffset + int64(last.Size)
}

// hashFile returns the lowercase hex SHA-256 of a segment.
func hashFile(root, rel string) (string, error) {
	abs, err := core.SafeJoin(root, rel)
	if err != nil {
		return "", err
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
