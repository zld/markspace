package verify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"replay/internal/manifest"
)

// fragmentPaths lists segment files (*.m4s) in dir, sorted by name.
func fragmentPaths(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".m4s") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

// probeTiming reads container duration and start time via ffprobe.
func probeTiming(path string) (dur, start time.Duration, err error) {
	cmd := exec.Command("ffprobe", "-v", "error", "-show_entries",
		"format=duration,start_time", "-of", "json", path)
	out, err := cmd.Output()
	if err != nil {
		return 0, 0, fmt.Errorf("verify: ffprobe %s: %w", path, err)
	}
	var res struct {
		Format struct {
			Duration string `json:"duration"`
			Start    string `json:"start_time"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return 0, 0, fmt.Errorf("verify: ffprobe output: %w", err)
	}
	parse := func(s string) (time.Duration, error) {
		if s == "" {
			return 0, nil
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, err
		}
		return time.Duration(f * float64(time.Second)), nil
	}
	if dur, err = parse(res.Format.Duration); err != nil {
		return 0, 0, err
	}
	if start, err = parse(res.Format.Start); err != nil {
		return 0, 0, err
	}
	return dur, start, nil
}

// psnrAgainstReference renders a high-quality reference of the same window
// straight from the input fragments and computes PSNR against the output.
func psnrAgainstReference(m *manifest.Manifest, setDir, outPath string) (float64, error) {
	tmp, err := os.MkdirTemp("", "replay-verify-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(tmp)

	// Rebuild the fMP4 stream independently: init + the recorded fragments.
	concatPath := filepath.Join(tmp, "concat.mp4")
	cf, err := os.Create(concatPath)
	if err != nil {
		return 0, err
	}
	for _, p := range append([]string{filepath.Join(setDir, "init.mp4")},
		segmentAbsPaths(m, setDir)...) {
		b, err := os.ReadFile(p)
		if err != nil {
			cf.Close()
			return 0, fmt.Errorf("verify: read %s: %w", p, err)
		}
		if _, err := cf.Write(b); err != nil {
			cf.Close()
			return 0, err
		}
	}
	cf.Close()

	// Remux to normalize the absolute tfdt timeline to zero, then cut the
	// window relative to the first segment's media start.
	remuxPath := filepath.Join(tmp, "remux.mp4")
	if out, err := exec.Command("ffmpeg", "-y", "-loglevel", "error",
		"-i", concatPath, "-c", "copy", remuxPath).CombinedOutput(); err != nil {
		return 0, fmt.Errorf("verify: remux: %w: %s", err, out)
	}
	relStartMS := m.Window.StartMS
	if len(m.Timeline.Segments) > 0 {
		relStartMS = m.Window.StartMS - m.Timeline.Segments[0].StartMS
		if relStartMS < 0 {
			relStartMS = 0
		}
	}

	refPath := filepath.Join(tmp, "ref.mp4")
	ss := strconv.FormatFloat(float64(relStartMS)/1000, 'f', 3, 64)
	dur := strconv.FormatFloat(float64(m.Window.EndMS-m.Window.StartMS)/1000, 'f', 3, 64)
	args := []string{"-y", "-loglevel", "error", "-ss", ss, "-i", remuxPath,
		"-t", dur, "-an", "-c:v", "libx264", "-qp", "0", "-pix_fmt", "yuv420p"}
	args = append(args, colorArgs(m)...)
	args = append(args, refPath)
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		return 0, fmt.Errorf("verify: reference render: %w: %s", err, out)
	}

	cmd := exec.Command("ffmpeg", "-i", outPath, "-i", refPath,
		"-lavfi", "[0:v][1:v]psnr", "-f", "null", "-")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("verify: psnr: %w: %s", err, stderr.String())
	}
	return ParsePSNRAverage(stderr.String())
}

// segmentAbsPaths resolves manifest segment paths against setDir.
func segmentAbsPaths(m *manifest.Manifest, setDir string) []string {
	out := make([]string, 0, len(m.Timeline.Segments))
	for _, s := range m.Timeline.Segments {
		if filepath.IsAbs(s.Path) {
			out = append(out, s.Path)
		} else {
			out = append(out, filepath.Join(setDir, s.Path))
		}
	}
	return out
}

// colorArgs returns the conversion flags matching the manifest's color
// decision, so the reference is rendered through the same transform.
func colorArgs(m *manifest.Manifest) []string {
	if m.Video.Converted && m.Video.ColorSource != nil {
		return []string{"-vf", "scale=out_color_matrix=bt709",
			"-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709"}
	}
	return nil
}
