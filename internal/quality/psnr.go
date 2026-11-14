// Package quality parses per-frame PSNR and computes segment and
// whole-title statistics.
package quality

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
)

var psnrAvgRe = regexp.MustCompile(`\bpsnr_avg:([0-9]+(?:\.[0-9]+)?|inf)\b`)

// ParsePSNRStats parses the per-frame stats file written by FFmpeg's psnr
// filter (lines like "n:1 mse_avg:... psnr_avg:42.531 ..."). Identical frames
// appear as "inf" and are returned as +Inf for the caller to saturate.
func ParsePSNRStats(r io.Reader) ([]float64, error) {
	var out []float64
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "n:") {
			return nil, fmt.Errorf("psnr stats line %d: unexpected content %q", lineNo, line)
		}
		m := psnrAvgRe.FindStringSubmatch(line)
		if m == nil {
			return nil, fmt.Errorf("psnr stats line %d: missing psnr_avg in %q", lineNo, line)
		}
		if m[1] == "inf" {
			out = append(out, math.Inf(1))
			continue
		}
		v, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return nil, fmt.Errorf("psnr stats line %d: %w", lineNo, err)
		}
		out = append(out, v)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
