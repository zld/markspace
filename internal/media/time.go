// Package media defines the unified media timeline, unit conversion,
// error model and input-path safety rules shared by every subsystem.
package media

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// TimelineRate is the single unified clock used by every output:
// 48 kHz PCM, one sample per tick.
const TimelineRate = 48000

// Operational limits from the processing boundary.
const (
	// MaxPacketBytes rejects any RTP packet / fMP4 sample larger than 1 MiB.
	MaxPacketBytes = 1 << 20
	// MaxPCMBytes bounds the in-memory normalized PCM cache at 64 MiB.
	// Stereo float32 = 8 bytes per frame, i.e. 8,388,608 frames.
	MaxPCMBytes = 64 << 20
)

// Range is a half-open interval on the unified 48 kHz timeline: [Start, End).
type Range struct {
	Start uint64 `json:"start"`
	End   uint64 `json:"end"`
}

// Valid reports whether the interval is well-formed.
func (r Range) Valid() bool { return r.End >= r.Start }

// Length returns the interval duration in timeline samples.
func (r Range) Length() uint64 {
	if r.End < r.Start {
		return 0
	}
	return r.End - r.Start
}

// Overlaps reports whether two intervals share at least one sample.
func (r Range) Overlaps(o Range) bool {
	return r.Start < o.End && o.Start < r.End
}

// Scale converts an absolute offset measured in sourceRate units to an
// absolute timeline offset, rounding to the nearest sample.
func Scale(units, sourceRate uint64) uint64 {
	if sourceRate == 0 {
		return 0
	}
	return (units*TimelineRate + sourceRate/2) / sourceRate
}

// RateConverter maps a stream running at SourceRate onto the unified
// timeline. It retains the fractional remainder of every conversion so
// that repeated conversions never accumulate rounding drift.
type RateConverter struct {
	SourceRate uint64
	acc        uint64 // unconsumed fractional part, in source units
	out        uint64 // samples already emitted on the timeline
}

// NewRateConverter builds a converter for the given source clock.
func NewRateConverter(sourceRate uint64) *RateConverter {
	return &RateConverter{SourceRate: sourceRate}
}

// Convert maps an increment of source units onto the timeline and returns
// the number of whole timeline samples it represents plus the new absolute
// output offset. The sub-sample remainder is retained for the next call.
func (c *RateConverter) Convert(deltaUnits uint64) (samples, outputOffset uint64) {
	if c.SourceRate == 0 {
		return 0, c.out
	}
	c.acc += deltaUnits * TimelineRate
	whole := c.acc / c.SourceRate
	c.acc %= c.SourceRate
	c.out += whole
	return whole, c.out
}

// OutputOffset reports the absolute timeline offset reached so far.
func (c *RateConverter) OutputOffset() uint64 { return c.out }

// Remainder reports the retained fractional source units (always < SourceRate).
func (c *RateConverter) Remainder() uint64 { return c.acc }

// SafeOpen opens an existing regular input file after rejecting path
// traversal outside root and any symbolic link on the path. The opened
// file is returned together with its absolute path.
func SafeOpen(root, name string) (*os.File, string, error) {
	if name == "" {
		return nil, "", errors.New("empty input path")
	}
	absRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return nil, "", err
	}
	absName, err := filepath.Abs(filepath.Clean(name))
	if err != nil {
		return nil, "", err
	}
	if absName != absRoot && !strings.HasPrefix(absName, absRoot+string(filepath.Separator)) {
		return nil, absName, errors.New("input path escapes the work root")
	}
	// Reject symlinks at or below the root. Ancestors above the root are
	// tolerated (on macOS the temp root itself lives under symlinked /var).
	rel, err := filepath.Rel(absRoot, absName)
	if err != nil {
		return nil, absName, err
	}
	cur := absRoot
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			return nil, absName, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, absName, errors.New("symbolic link inputs are rejected")
		}
	}
	info, err := os.Lstat(absName)
	if err != nil {
		return nil, absName, err
	}
	if !info.Mode().IsRegular() {
		return nil, absName, errors.New("input is not a regular file")
	}
	f, err := os.Open(absName)
	if err != nil {
		return nil, absName, err
	}
	return f, absName, nil
}
