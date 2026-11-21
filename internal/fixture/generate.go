package fixture

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"streamcheck/internal/manifest"
)

// fixedTime is used for every generated file so repeated runs produce
// identical modification times.
var fixedTime = time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)

// FixedTime returns the deterministic modification time used by fixtures.
func FixedTime() time.Time { return fixedTime }

// WriteTS writes one TS segment to path and sets a deterministic mtime.
func WriteTS(path string, durationMS int64, paddingBytes int) error {
	data, err := TS(durationMS, paddingBytes)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	return os.Chtimes(path, FixedTime(), FixedTime())
}

// Spec describes the synthetic data set to generate.
type Spec struct {
	Out            string
	Rates          []int
	Segments       int
	SegmentSeconds float64
	Pattern        string
}

// SegmentMS is the nominal duration of one generated segment.
func (s Spec) SegmentMS() int64 { return int64(s.SegmentSeconds*1000 + 0.5) }

// Validate checks a spec before any file is written.
func (s Spec) Validate() error {
	if strings.TrimSpace(s.Out) == "" {
		return fmt.Errorf("output directory is required")
	}
	if len(s.Rates) == 0 {
		return fmt.Errorf("at least one bitrate is required")
	}
	if s.Segments <= 0 {
		return fmt.Errorf("segments must be positive")
	}
	if s.SegmentSeconds <= 0 || math.IsNaN(s.SegmentSeconds) || math.IsInf(s.SegmentSeconds, 0) {
		return fmt.Errorf("segment seconds must be positive")
	}
	pattern, err := manifest.CompilePattern(s.Pattern)
	if err != nil {
		return err
	}
	if len(s.Rates) > 1 && !pattern.HasRate() {
		return fmt.Errorf("pattern %q must contain {rate} for multiple bitrates", s.Pattern)
	}
	return nil
}

// paddingBytes returns a deterministic size difference per bitrate, aligned
// to whole TS packets so higher bitrates produce larger files.
func paddingBytes(rateKbps int) int {
	pad := rateKbps / 10 * 188
	return pad - pad%(188-4)
}

// mainDurationMS returns the duration for one segment. The per-id jitter
// exercises boundary comparison and the per-rate offset makes renditions
// drift slightly, while the total stays well under the default 80ms
// tolerance so the happy path still passes.
func (s Spec) mainDurationMS(rate, id int) int64 {
	return s.SegmentMS() + int64((id%3)-1)*3 + int64((rate/400)%3-1)*2
}

// Generate writes the happy-path fixture plus the missing, duplicate,
// unordered and drift cases under Out/cases.
func Generate(s Spec) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.Out, 0o755); err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	if err := s.writeAll(s.Out, s.Segments, func(int, int) (int64, bool) { return 0, true }, false); err != nil {
		return fmt.Errorf("fixture: %w", err)
	}
	cases := []struct {
		name string
		run  func(dir string) error
	}{
		{"missing", s.caseMissing},
		{"duplicate", s.caseDuplicate},
		{"unordered", s.caseUnordered},
		{"drift", s.caseDrift},
	}
	for _, c := range cases {
		dir := filepath.Join(s.Out, "cases", c.name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create case %s: %w", c.name, err)
		}
		if err := c.run(dir); err != nil {
			return fmt.Errorf("case %s: %w", c.name, err)
		}
	}
	return nil
}

// writeAll writes segments 0..count-1 for every rate. duration may override
// the duration for an id; skip reports whether to omit the file. When
// reverse is true, ids are written from high to low.
func (s Spec) writeAll(dir string, count int, duration func(rate, id int) (int64, bool), reverse bool) error {
	for _, rate := range s.Rates {
		for step := 0; step < count; step++ {
			id := step
			if reverse {
				id = count - 1 - step
			}
			d, keep := duration(rate, id)
			if !keep {
				continue
			}
			if d == 0 {
				d = s.mainDurationMS(rate, id)
			}
			name := manifest.SegmentFileName(s.Pattern, rate, id)
			if err := WriteTS(filepath.Join(dir, name), d, paddingBytes(rate)); err != nil {
				return fmt.Errorf("write %s: %w", name, err)
			}
		}
	}
	return nil
}

func (s Spec) caseMissing(dir string) error {
	skip := map[int]bool{s.Segments / 2: true, s.Segments/2 + 1: true}
	return s.writeAll(dir, s.Segments, func(_ int, id int) (int64, bool) { return 0, !skip[id] }, false)
}

// duplicateName is the same rule with the index zero-padded, which is a
// realistic leftover from a previous batch and still matches the pattern.
func (s Spec) duplicateName(rate, id int) string {
	pattern := strings.ReplaceAll(s.Pattern, "{index}", fmt.Sprintf("%02d", id))
	return strings.ReplaceAll(pattern, "{rate}", fmt.Sprintf("%d", rate))
}

func (s Spec) caseDuplicate(dir string) error {
	if err := s.writeAll(dir, s.Segments, func(int, int) (int64, bool) { return 0, true }, false); err != nil {
		return err
	}
	id := 1
	if s.Segments <= id {
		id = s.Segments - 1
	}
	for _, rate := range s.Rates {
		name := s.duplicateName(rate, id)
		if name == manifest.SegmentFileName(s.Pattern, rate, id) {
			return fmt.Errorf("cannot build a duplicate name for pattern %q", s.Pattern)
		}
		if err := WriteTS(filepath.Join(dir, name), s.mainDurationMS(rate, id), paddingBytes(rate)); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return nil
}

func (s Spec) caseUnordered(dir string) error {
	// Twelve ids make lexicographic and numeric order differ (10 after 2).
	count := s.Segments
	if count < 12 {
		count = 12
	}
	return s.writeAll(dir, count, func(int, int) (int64, bool) { return 0, true }, true)
}

func (s Spec) caseDrift(dir string) error {
	// Only rates above the reference drift, so cross-rate comparison has
	// something to report; the extra 250ms exceeds the default tolerance.
	minRate := s.Rates[0]
	for _, r := range s.Rates[1:] {
		if r < minRate {
			minRate = r
		}
	}
	last := s.Segments - 1
	return s.writeAll(dir, s.Segments, func(rate, id int) (int64, bool) {
		if id == last && rate > minRate {
			return s.mainDurationMS(rate, id) + 250, true
		}
		return 0, true
	}, false)
}
