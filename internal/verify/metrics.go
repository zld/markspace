// Package verify computes quantitative acceptance metrics: bitrate deviation,
// maximum timeline error and PSNR. PSNR is produced by the local FFmpeg binary
// through an injectable runner so the numeric logic stays unit-testable
// without spawning a video encoder.
package verify

import (
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
)

// Metrics is the quantitative result reported per scenario.
type Metrics struct {
	TargetKbps      int     `json:"target_kbps"`
	ActualKbps      int     `json:"actual_kbps"`
	BitrateDeltaPct float64 `json:"bitrate_delta_pct"`
	PSNRDB          float64 `json:"psnr_db"`
	MaxTimeErrorMs  float64 `json:"manifest_max_error_ms"`
}

// BitrateMetrics derives actual kbps from delivered bytes and duration, then
// the signed percentage deviation from the target. A non-positive target or
// duration is rejected rather than dividing by zero.
func BitrateMetrics(actualBytes int64, durationNanos int64, targetKbps int) (Metrics, error) {
	if targetKbps <= 0 {
		return Metrics{}, errors.New("verify: target kbps must be positive")
	}
	if durationNanos <= 0 {
		return Metrics{}, errors.New("verify: duration must be positive")
	}
	seconds := float64(durationNanos) / 1e9
	actualKbps := int(math.Round(float64(actualBytes) * 8.0 / seconds / 1000.0))
	if actualKbps < 0 {
		return Metrics{}, errors.New("verify: negative actual bitrate")
	}
	delta := (float64(actualKbps) - float64(targetKbps)) / float64(targetKbps) * 100.0
	return Metrics{
		TargetKbps:      targetKbps,
		ActualKbps:      actualKbps,
		BitrateDeltaPct: round2(delta),
	}, nil
}

// MaxTimeError returns the largest absolute difference between expected and
// actual nanosecond timestamps, in milliseconds. The two slices must align by
// index; a length mismatch is an error because it indicates a missing event.
func MaxTimeError(expectedNanos, actualNanos []int64) (float64, error) {
	if len(expectedNanos) != len(actualNanos) {
		return 0, fmt.Errorf("verify: timestamp count mismatch: %d vs %d",
			len(expectedNanos), len(actualNanos))
	}
	var maxNs int64
	for i := range expectedNanos {
		d := expectedNanos[i] - actualNanos[i]
		if d < 0 {
			d = -d
		}
		if d > maxNs {
			maxNs = d
		}
	}
	return round2(float64(maxNs) / 1e6), nil
}

// PSNR computes the peak signal-to-noise ratio (dB) between a reference and a
// distorted video using the local FFmpeg PSNR filter. A fully identical pair
// yields +Inf which callers render as the sentinel PSNRIdentical.
const PSNRIdentical = math.MaxFloat64

// Runner abstracts command execution for testability.
type Runner interface {
	// PSNR runs ffmpeg comparing refPath against distPath and returns average
	// PSNR in dB, or PSNRIdentical for lossless equality.
	PSNR(refPath, distPath string) (float64, error)
}

// FFmpegRunner invokes the real ffmpeg binary found on PATH.
type FFmpegRunner struct {
	Bin string // defaults to "ffmpeg"
}

func (r FFmpegRunner) bin() string {
	if r.Bin == "" {
		return "ffmpeg"
	}
	return r.Bin
}

// PSNR shells out to ffmpeg -i dist -i ref -lavfi psnr and parses the summary
// line "PSNR avg:xx.xx ...". No network or remote material is involved.
func (r FFmpegRunner) PSNR(refPath, distPath string) (float64, error) {
	filter := "psnr"
	cmd := exec.Command(r.bin(),
		"-hide_banner", "-nostdin",
		"-i", distPath,
		"-i", refPath,
		"-lavfi", filter,
		"-f", "null", "-",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("verify: ffmpeg psnr: %w: %s", err, tailLines(out))
	}
	return parsePSNR(string(out))
}

// parsePSNR extracts "average:NN.NN" from ffmpeg's PSNR summary. ffmpeg prints
// the summary on stderr in a line ending e.g. "PSNR y:.. u:.. v:.. average:..".
func parsePSNR(output string) (float64, error) {
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, "average:") {
			continue
		}
		idx := strings.LastIndex(line, "average:")
		field := line[idx+len("average:"):]
		field = strings.Fields(field)[0]
		// "inf" means the signals are bit-identical.
		if strings.HasPrefix(field, "inf") {
			return PSNRIdentical, nil
		}
		v, err := strconv.ParseFloat(field, 64)
		if err != nil {
			return 0, fmt.Errorf("verify: parse psnr %q: %w", field, err)
		}
		return v, nil
	}
	return 0, errors.New("verify: ffmpeg output contained no PSNR summary")
}

// FramePSNR is a pure-Go PSNR over 8-bit planar luma samples used for unit
// tests and offline verification without an encoder. Both planes must share
// dimensions; maxVal is the signal maximum (255 for 8-bit).
func FramePSNR(ref, dist []byte) (float64, error) {
	if len(ref) != len(dist) {
		return 0, fmt.Errorf("verify: plane size mismatch %d vs %d", len(ref), len(dist))
	}
	if len(ref) == 0 {
		return 0, errors.New("verify: empty plane")
	}
	var mse float64
	for i := range ref {
		d := float64(ref[i]) - float64(dist[i])
		mse += d * d
	}
	mse /= float64(len(ref))
	if mse == 0 {
		return PSNRIdentical, nil
	}
	return round2(10 * math.Log10((255.0*255.0)/mse)), nil
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

// Acceptance thresholds from the delivery contract.
const (
	MaxBitrateDeltaPct = 10.0
	MinPSNRDB          = 30.0
	MaxTimeErrorMs     = 1.0
)

// BitrateWithinTolerance reports whether an absolute bitrate deviation is
// within pct percent.
func BitrateWithinTolerance(deltaPct, pct float64) bool {
	if deltaPct < 0 {
		deltaPct = -deltaPct
	}
	return deltaPct <= pct
}

// TimeErrorWithinTolerance reports whether a max timing error (ms) is within
// the bound.
func TimeErrorWithinTolerance(errMs, boundMs float64) bool {
	if errMs < 0 {
		errMs = -errMs
	}
	return errMs <= boundMs
}

// AcceptanceFailures returns a human-readable reason for each failed
// acceptance criterion; an empty slice means the scenario fully passes. An
// infinite PSNR (lossless pair) always satisfies the minimum.
func AcceptanceFailures(m Metrics) []string {
	var fails []string
	if !BitrateWithinTolerance(m.BitrateDeltaPct, MaxBitrateDeltaPct) {
		fails = append(fails, fmt.Sprintf(
			"bitrate delta %.2f%% exceeds %.0f%%", m.BitrateDeltaPct, MaxBitrateDeltaPct))
	}
	if !(m.PSNRDB >= MinPSNRDB) {
		fails = append(fails, fmt.Sprintf(
			"psnr %.2f dB below %.0f dB", m.PSNRDB, MinPSNRDB))
	}
	if !TimeErrorWithinTolerance(m.MaxTimeErrorMs, MaxTimeErrorMs) {
		fails = append(fails, fmt.Sprintf(
			"max time error %.2f ms exceeds %.0f ms", m.MaxTimeErrorMs, MaxTimeErrorMs))
	}
	return fails
}

func tailLines(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 400 {
		s = "..." + s[len(s)-400:]
	}
	return s
}
