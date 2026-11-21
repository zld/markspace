// Package verify re-reads the HLS artefacts produced by the build pipeline
// using the shared m3u8 model and checks segment existence, hashes, timing,
// bitrate, ordering and cross-tier boundary alignment.
package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"streamcheck/internal/m3u8"
)

// FailureCode enumerates the machine-readable verification failure kinds.
type FailureCode string

const (
	// FailMissingSegment: a playlist references a file that does not exist.
	FailMissingSegment FailureCode = "missing_segment"
	// FailHashMismatch: a segment's SHA-256 differs from the sidecar value.
	FailHashMismatch FailureCode = "hash_mismatch"
	// FailOutOfOrder: media sequence numbers are not consecutive.
	FailOutOfOrder FailureCode = "out_of_order"
	// FailDuplicate: two entries reference the same segment URI.
	FailDuplicate FailureCode = "duplicate_segment"
	// FailDuration: a segment duration strays outside the allowed window.
	FailDuration FailureCode = "duration_deviation"
	// FailBitrate: the measured bitrate strays outside tolerance.
	FailBitrate FailureCode = "bitrate_deviation"
	// FailCrossTier: per-tier segment boundaries diverge across renditions.
	FailCrossTier FailureCode = "cross_tier_boundary"
	// FailPlaylist: the playlist itself cannot be parsed.
	FailPlaylist FailureCode = "playlist_error"
)

// Failure is one locatable verification problem.
type Failure struct {
	Code     FailureCode `json:"code"`
	Tier     string      `json:"tier"`
	Segment  string      `json:"segment,omitempty"`
	Sequence int64       `json:"sequence,omitempty"`
	Message  string      `json:"message"`
}

// TierResult holds the verification outcome for one rendition.
type TierResult struct {
	Tier        string    `json:"tier"`
	BitrateBPS  int       `json:"bitrate_bps"`
	Segments    int       `json:"segments"`
	DurationMS  int64     `json:"duration_ms"`
	DeviationMS int64     `json:"duration_deviation_ms"`
	Failures    []Failure `json:"failures"`
}

// HasFailures reports whether any failure was recorded.
func (r TierResult) HasFailures() bool { return len(r.Failures) > 0 }

// TierSpec carries the build-time expectation for one rendition.
type TierSpec struct {
	Name       string
	BitrateBPS int
	SegmentMS  int64
	// Sidecar maps a segment file name to its expected hex SHA-256.
	Sidecar map[string]string
}

// Checker validates tier directories beneath a single output root.
type Checker struct {
	root       string
	tolerance  time.Duration
	bitrateTol float64
}

// NewChecker builds a checker. root is the output directory; every accessed
// path is confined beneath it.
func NewChecker(root string, tolerance time.Duration, bitrateTol float64) *Checker {
	if tolerance <= 0 {
		tolerance = 50 * time.Millisecond
	}
	if bitrateTol <= 0 {
		bitrateTol = 0.15
	}
	return &Checker{root: root, tolerance: tolerance, bitrateTol: bitrateTol}
}

// CheckTier parses <root>/<tier>/index.m3u8 and validates every segment.
func (c *Checker) CheckTier(spec TierSpec) TierResult {
	res := TierResult{Tier: spec.Name, BitrateBPS: spec.BitrateBPS}
	plPath, err := c.resolve(spec.Name, "index.m3u8")
	if err != nil {
		return c.playlistError(res, spec.Name, err)
	}
	f, err := os.Open(plPath)
	if err != nil {
		return c.playlistError(res, spec.Name, err)
	}
	defer f.Close()
	pl, err := m3u8.ParseMedia(f)
	if err != nil {
		return c.playlistError(res, spec.Name, err)
	}
	res.Segments = len(pl.Segments)
	res.DurationMS = pl.DurationMS()

	seen := make(map[string]int)
	wantSeq := int64(pl.MediaSequence)
	var wantFileSeq int64 = -1
	var maxDev int64
	for i, seg := range pl.Segments {
		if int64(seg.Sequence) != wantSeq {
			res.Failures = append(res.Failures, Failure{
				Code: FailOutOfOrder, Tier: spec.Name, Segment: seg.URI,
				Sequence: int64(seg.Sequence),
				Message: fmt.Sprintf("expected sequence %d, got %d",
					wantSeq, seg.Sequence),
			})
		}
		wantSeq++
		// Detect gaps and reordering encoded in the segment file names,
		// which a re-numbered playlist sequence alone would hide.
		if fileSeq, ok := segmentFileSequence(seg.URI); ok {
			if wantFileSeq >= 0 && fileSeq != wantFileSeq {
				res.Failures = append(res.Failures, Failure{
					Code: FailOutOfOrder, Tier: spec.Name, Segment: seg.URI,
					Sequence: fileSeq,
					Message: fmt.Sprintf("segment file sequence %d follows %d",
						fileSeq, wantFileSeq-1),
				})
			}
			wantFileSeq = fileSeq + 1
		}
		if prev, ok := seen[seg.URI]; ok {
			res.Failures = append(res.Failures, Failure{
				Code: FailDuplicate, Tier: spec.Name, Segment: seg.URI,
				Sequence: int64(seg.Sequence),
				Message:  fmt.Sprintf("duplicate of segment #%d", prev),
			})
		}
		seen[seg.URI] = i
		fails, dev := c.checkSegment(spec, seg)
		res.Failures = append(res.Failures, fails...)
		if dev > maxDev {
			maxDev = dev
		}
	}
	res.DeviationMS = maxDev
	return res
}

func (c *Checker) playlistError(res TierResult, tier string, err error) TierResult {
	res.Failures = append(res.Failures, Failure{
		Code: FailPlaylist, Tier: tier, Message: err.Error(),
	})
	return res
}

// checkSegment validates one segment and returns its failures plus the
// absolute duration deviation in milliseconds.
func (c *Checker) checkSegment(spec TierSpec, seg m3u8.Segment) ([]Failure, int64) {
	var fails []Failure
	path, err := c.resolve(spec.Name, seg.URI)
	if err != nil {
		return []Failure{c.makeFail(spec.Name, seg, FailPlaylist, err.Error())}, 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return []Failure{c.makeFail(spec.Name, seg, FailMissingSegment,
			fmt.Sprintf("segment file %q not found", seg.URI))}, 0
	}
	if want, ok := spec.Sidecar[seg.URI]; ok {
		got, err := hashFile(path)
		if err != nil {
			fails = append(fails, c.makeFail(spec.Name, seg, FailHashMismatch, err.Error()))
		} else if got != want {
			fails = append(fails, c.makeFail(spec.Name, seg, FailHashMismatch,
				fmt.Sprintf("sha256 %s != expected %s", got, want)))
		}
	}
	var maxDev int64
	if spec.SegmentMS > 0 {
		nominal := time.Duration(spec.SegmentMS) * time.Millisecond
		dev := seg.Duration - nominal
		if dev < 0 {
			dev = -dev
		}
		maxDev = dev.Milliseconds()
		if dev > c.tolerance {
			fails = append(fails, c.makeFail(spec.Name, seg, FailDuration,
				fmt.Sprintf("duration %v deviates %v from nominal %v",
					seg.Duration.Round(time.Millisecond),
					dev.Round(time.Millisecond), nominal)))
		}
		if seg.Duration > 0 && spec.BitrateBPS > 0 {
			got := float64(info.Size()) * 8.0 / seg.Duration.Seconds()
			diff := (got - float64(spec.BitrateBPS)) / float64(spec.BitrateBPS)
			if diff > c.bitrateTol || diff < -c.bitrateTol {
				fails = append(fails, c.makeFail(spec.Name, seg, FailBitrate,
					fmt.Sprintf("bitrate %.0f bps differs by %.1f%%", got, diff*100)))
			}
		}
	}
	return fails, maxDev
}

func (c *Checker) makeFail(tier string, seg m3u8.Segment, code FailureCode, msg string) Failure {
	return Failure{
		Code: code, Tier: tier, Segment: seg.URI,
		Sequence: int64(seg.Sequence), Message: msg,
	}
}

// CheckCrossTier compares segment counts and boundary durations across
// renditions, flagging tiers whose boundaries diverge beyond tolerance.
func (c *Checker) CheckCrossTier(results []TierResult) []Failure {
	var fails []Failure
	if len(results) < 2 {
		return fails
	}
	ref := results[0]
	for _, r := range results[1:] {
		if r.Segments != ref.Segments {
			fails = append(fails, Failure{
				Code: FailCrossTier, Tier: r.Tier,
				Message: fmt.Sprintf("segment count %d != reference %d (%s)",
					r.Segments, ref.Segments, ref.Tier),
			})
		}
		diff := r.DurationMS - ref.DurationMS
		if diff < 0 {
			diff = -diff
		}
		if diff > c.tolerance.Milliseconds() {
			fails = append(fails, Failure{
				Code: FailCrossTier, Tier: r.Tier,
				Message: fmt.Sprintf("total duration %dms differs from %s by %dms",
					r.DurationMS, ref.Tier, diff),
			})
		}
	}
	return fails
}

// resolve confines a relative path to the output directory.
func (c *Checker) resolve(parts ...string) (string, error) {
	joined := filepath.Join(parts...)
	clean := filepath.Clean(joined)
	if filepath.IsAbs(clean) || startsDotDot(clean) {
		return "", fmt.Errorf("verify: path %q escapes output directory", joined)
	}
	return filepath.Join(c.root, clean), nil
}

func startsDotDot(p string) bool {
	return p == ".." || filepath.HasPrefix(p, ".."+string(filepath.Separator))
}

// segmentFileSequence extracts a numeric index embedded in a segment file
// name such as "seg-00004.ts". The second return reports whether the name
// contained a parseable numeric run.
func segmentFileSequence(uri string) (int64, bool) {
	base := filepath.Base(uri)
	var digits []byte
	for i := 0; i < len(base); i++ {
		if base[i] >= '0' && base[i] <= '9' {
			digits = append(digits, base[i])
		}
	}
	if len(digits) == 0 {
		return 0, false
	}
	var n int64
	for _, d := range digits {
		n = n*10 + int64(d-'0')
	}
	return n, true
}

func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
