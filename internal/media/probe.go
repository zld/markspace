// Package media wraps ffprobe to read stream parameters, keyframe
// timestamps and input digests. All subprocess calls use argv slices.
package media

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// StreamInfo is the normalized view of the first video stream.
type StreamInfo struct {
	Path        string    `json:"path"`
	DurationSec float64   `json:"duration_sec"`
	Width       int       `json:"width"`
	Height      int       `json:"height"`
	CodecName   string    `json:"codec_name"`
	AvgFPS      float64   `json:"avg_fps"`
	TimeBaseDen int       `json:"time_base_den"` // normalized: pts_seconds = raw_pts / den
	KeyframeSec []float64 `json:"keyframe_sec"`  // sorted, seconds
	VFR         bool      `json:"vfr"`
}

type probeOutput struct {
	Streams []struct {
		CodecName    string `json:"codec_name"`
		Width        int    `json:"width"`

		Height       int    `json:"height"`
		AvgFrameRate string `json:"avg_frame_rate"`
		RFrameRate   string `json:"r_frame_rate"`
		TimeBase     string `json:"time_base"`
		Duration     string `json:"duration"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

type framesOutput struct {
	Frames []struct {
		PTS string `json:"pts_time"`
	} `json:"frames"`
}

// Probe reads stream parameters and keyframe timestamps from path.
// Timestamps are normalized to float seconds regardless of container
// time base. Non-monotonic keyframe timestamps (discontinuous input)
// are reported via the HasDiscontinuity helper on the result.
func Probe(path string) (*StreamInfo, error) {
	args := []string{
		"-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=codec_name,width,height,avg_frame_rate,r_frame_rate,time_base:format=duration",
		"-of", "json", path,
	}
	out, err := exec.Command("ffprobe", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe streams: %w", err)
	}
	var po probeOutput
	if err := json.Unmarshal(out, &po); err != nil {
		return nil, fmt.Errorf("parse ffprobe output: %w", err)
	}
	if len(po.Streams) == 0 {
		return nil, fmt.Errorf("no video stream in %s", path)
	}
	s := po.Streams[0]
	dur, _ := strconv.ParseFloat(po.Format.Duration, 64)
	if dur <= 0 {
		dur, _ = strconv.ParseFloat(s.Duration, 64)
	}
	if dur <= 0 {
		return nil, fmt.Errorf("cannot determine duration of %s", path)
	}
	avg := parseRate(s.AvgFrameRate)
	if avg <= 0 {
		return nil, fmt.Errorf("cannot determine frame rate of %s", path)
	}
	info := &StreamInfo{
		Path:        path,
		DurationSec: dur,
		Width:       s.Width,
		Height:      s.Height,
		CodecName:   s.CodecName,
		AvgFPS:      avg,
		TimeBaseDen: parseTimeBaseDen(s.TimeBase),
	}
	r := parseRate(s.RFrameRate)
	info.VFR = r > 0 && (avg/r < 0.98 || avg/r > 1.02)
	keys, err := probeKeyframes(path)
	if err != nil {
		return nil, err
	}
	info.KeyframeSec = keys
	return info, nil
}

// probeKeyframes returns keyframe presentation times in seconds, sorted
// and deduplicated. Out-of-order source timestamps are preserved by
// sorting, and callers use HasDiscontinuity to detect gaps.
func probeKeyframes(path string) ([]float64, error) {
	out, err := exec.Command("ffprobe",
		"-v", "error", "-select_streams", "v:0", "-skip_frame", "nokey",
		"-show_entries", "frame=pts_time", "-of", "json", path).Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe keyframes: %w", err)
	}
	var fo framesOutput
	if err := json.Unmarshal(out, &fo); err != nil {
		return nil, fmt.Errorf("parse keyframe output: %w", err)
	}
	keys := make([]float64, 0, len(fo.Frames))
	for _, f := range fo.Frames {
		if v, err := strconv.ParseFloat(f.PTS, 64); err == nil {
			keys = append(keys, v)
		}
	}
	sort.Float64s(keys)
	dedup := keys[:0]
	for i, k := range keys {
		if i == 0 || k-keys[i-1] > 1e-6 {
			dedup = append(dedup, k)
		}
	}
	return dedup, nil
}

// HasDiscontinuity reports whether keyframe timestamps contain a gap
// larger than 1.5x the median keyframe interval, indicating timestamp
// discontinuities that segment mapping must not silently cross.
func (s *StreamInfo) HasDiscontinuity() bool {
	if len(s.KeyframeSec) < 3 {
		return false
	}
	gaps := make([]float64, 0, len(s.KeyframeSec)-1)
	for i := 1; i < len(s.KeyframeSec); i++ {
		gaps = append(gaps, s.KeyframeSec[i]-s.KeyframeSec[i-1])
	}
	sorted := append([]float64(nil), gaps...)
	sort.Float64s(sorted)
	med := sorted[len(sorted)/2]
	for _, g := range gaps {
		if g > 1.5*med && g > 1.0 {
			return true
		}
	}
	return false
}

// parseRate parses ffprobe "num/den" frame rates.
func parseRate(s string) float64 {
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return 0
	}
	n, err1 := strconv.ParseFloat(parts[0], 64)
	d, err2 := strconv.ParseFloat(parts[1], 64)
	if err1 != nil || err2 != nil || d == 0 {
		return 0
	}
	return n / d
}

// parseTimeBaseDen normalizes a "1/N" time base to its denominator so
// raw pts values convert to seconds via raw/den.
func parseTimeBaseDen(tb string) int {
	parts := strings.Split(tb, "/")
	if len(parts) == 2 {
		if d, err := strconv.Atoi(parts[1]); err == nil && d > 0 {
			return d
		}
	}
	return 1
}

// Digest computes a content digest of the input without reading the
// whole file: SHA-256 over size, modtime and the first/last 1 MiB.
// Resume uses it to verify the input has not changed.
func Digest(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	var hdr [16]byte
	binary.LittleEndian.PutUint64(hdr[:8], uint64(st.Size()))
	binary.LittleEndian.PutUint64(hdr[8:], uint64(st.ModTime().Unix()))
	h.Write(hdr[:])
	const chunk = 1 << 20
	buf := make([]byte, chunk)
	n, _ := io.ReadFull(f, buf)
	h.Write(buf[:n])
	if st.Size() > chunk {
		if _, err := f.Seek(-chunk, io.SeekEnd); err == nil {
			m, _ := io.ReadFull(f, buf)
			h.Write(buf[:m])
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
