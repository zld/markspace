// Package check re-reads published HLS output and verifies it against
// the manifest: missing segments, ordering, SHA-256 hashes, PTS gaps
// and per-segment duration drift. It collects all evidence instead of
// stopping at the first error.
package check

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"hlscheck/internal/hls"
	"hlscheck/internal/ts"
)

// gapLimitTicks flags PTS discontinuities above 200 ms.
const gapLimitTicks = ts.ClockRate / 5

// ProfileReport aggregates every defect found in one profile directory.
type ProfileReport struct {
	Name             string        `json:"name"`
	BitrateKbps      int           `json:"bitrate_kbps"`
	Segments         int           `json:"segments"` // entries declared by the manifest
	DurationDeltaMs  float64       `json:"duration_delta_ms"`
	Missing          []int         `json:"missing,omitempty"`
	Reordered        int           `json:"reordered"`
	ChecksumFailures []int         `json:"checksum_failures,omitempty"`
	PTSGaps          []string      `json:"pts_gaps,omitempty"`
	Errors           []string      `json:"errors,omitempty"`
	Starts           map[int]int64 `json:"-"` // manifest seq -> first unwrapped PTS
}

// OK reports whether the profile is clean.
func (r *ProfileReport) OK() bool {
	return len(r.Errors) == 0 && len(r.Missing) == 0 && r.Reordered == 0 &&
		len(r.ChecksumFailures) == 0 && len(r.PTSGaps) == 0
}

// CheckProfile verifies dir/index.m3u8 and its segment files.
func CheckProfile(dir string) *ProfileReport {
	rep := &ProfileReport{Name: filepath.Base(dir), Starts: map[int]int64{}}
	if n, err := strconv.Atoi(rep.Name); err == nil {
		rep.BitrateKbps = n
	}
	data, err := os.ReadFile(filepath.Join(dir, "index.m3u8"))
	if err != nil {
		rep.Errors = append(rep.Errors, err.Error())
		return rep
	}
	pl, err := hls.Decode(bytes.NewReader(data))
	if err != nil {
		rep.Errors = append(rep.Errors, err.Error())
		return rep
	}
	rep.Segments = len(pl.Segments)
	var prevEnd int64
	var havePrev bool
	for i, seg := range pl.Segments {
		path := filepath.Join(dir, seg.URI)
		if _, err := os.Stat(path); err != nil {
			rep.Missing = append(rep.Missing, seg.Seq)
			continue
		}
		sum, err := hashFile(path)
		if err != nil {
			rep.Errors = append(rep.Errors, err.Error())
			continue
		}
		if sum != seg.SHA256 {
			rep.ChecksumFailures = append(rep.ChecksumFailures, seg.Seq)
			rep.noteReorder(pl, i, sum)
		}
		first, last, count, anomalies, err := segmentPTS(path)
		if err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("%s: %v", seg.URI, err))
			continue
		}
		for _, a := range anomalies {
			rep.Errors = append(rep.Errors, fmt.Sprintf("%s: %s", seg.URI, a))
		}
		rep.Starts[seg.Seq] = first
		if havePrev {
			if gap := first - prevEnd; gap > gapLimitTicks {
				rep.PTSGaps = append(rep.PTSGaps,
					fmt.Sprintf("gap %.0f ms before seq %d", float64(gap)/90, seg.Seq))
			} else if gap < -gapLimitTicks {
				rep.PTSGaps = append(rep.PTSGaps,
					fmt.Sprintf("overlap %.0f ms before seq %d", float64(-gap)/90, seg.Seq))
			}
		}
		prevEnd, havePrev = last, true
		measured := 0.0
		if count > 1 {
			frameInt := float64(last-first) / float64(count-1)
			measured = (float64(last-first) + frameInt) / ts.ClockRate
		}
		if d := abs(measured-seg.Duration) * 1000; d > rep.DurationDeltaMs {
			rep.DurationDeltaMs = d
		}
	}
	return rep
}

// noteReorder detects adjacent transpositions: when a file's hash
// matches the next manifest entry, the pair is counted once.
func (r *ProfileReport) noteReorder(pl *hls.Playlist, i int, sum string) {
	if i+1 < len(pl.Segments) && sum == pl.Segments[i+1].SHA256 {
		r.Reordered++
		return
	}
	if i > 0 && sum == pl.Segments[i-1].SHA256 {
		return // already counted by the neighbour
	}
	for j, other := range pl.Segments {
		if j != i && sum == other.SHA256 {
			r.Errors = append(r.Errors,
				fmt.Sprintf("file %s holds content of seq %d", pl.Segments[i].URI, other.Seq))
			return
		}
	}
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
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

func segmentPTS(path string) (first, last int64, count int, anomalies []ts.Anomaly, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, 0, nil, err
	}
	defer f.Close()
	first, last, count, anomalies, err = ts.ScanPTS(f)
	return first, last, count, anomalies, err
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
