// Package subtitle parses SRT and WebVTT subtitle tracks onto the shared
// nanosecond scale. Both parsers report line numbers, reject negative
// durations, illegal timestamps and over-long cue text, and share the same
// hard limits as the rest of the pipeline.
package subtitle

import (
	"bufio"
	"io"
	"strconv"
	"strings"

	"mediaverify/internal/clock"
	"mediaverify/internal/errs"
)

// Limits enforced while parsing. MaxTextBytes caps a single cue's text and
// MaxAssetChars caps the cumulative track size (the 1M-character limit).
const (
	MaxTextBytes  = 16 * 1024
	MaxAssetChars = 1_000_000
)

// Cue is one subtitle interval on the shared nanosecond scale.
type Cue struct {
	Index    int         // 1-based appearance index
	Start    clock.Ticks // inclusive start
	End      clock.Ticks // exclusive end
	Lines    []string    // raw payload lines, never rewritten
	FirstRow int         // 1-based line number of the cue header
}

// Config carries the per-track parsing limits.
type Config struct {
	MaxTextBytes  int
	MaxAssetChars int
}

// DefaultConfig returns the production limits.
func DefaultConfig() Config {
	return Config{MaxTextBytes: MaxTextBytes, MaxAssetChars: MaxAssetChars}
}

// counter accumulates the 1M-character budget across a whole track.
type counter struct {
	used int
	cfg  Config
}

func (c *counter) add(s string) error {
	c.used += len(s)
	if c.used > c.cfg.MaxAssetChars {
		return errs.New(errs.KindLimitExceeded, "subtitle",
			"track exceeds %d characters", c.cfg.MaxAssetChars)
	}
	return nil
}

// parseClock converts HH:MM:SS.mmm (SRT) or HH:MM:SS.mmm / MM:SS.mmm
// (WebVTT) into nanoseconds.
func parseClock(raw string) (clock.Ticks, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, errs.New(errs.KindInvalidInput, "subtitle", "empty timestamp")
	}
	parts := strings.Split(raw, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, errs.New(errs.KindInvalidInput, "subtitle", "bad timestamp %q", raw)
	}
	var h, m int
	secIdx := 0
	if len(parts) == 3 {
		n, err := strconv.Atoi(parts[0])
		if err != nil || n < 0 {
			return 0, errs.New(errs.KindInvalidInput, "subtitle", "bad hour in %q", raw)
		}
		h = n
		secIdx = 1
	}
	n, err := strconv.Atoi(parts[secIdx])
	if err != nil || n < 0 || n > 59 {
		return 0, errs.New(errs.KindInvalidInput, "subtitle", "bad minute in %q", raw)
	}
	m = n
	// SRT uses ',' as the decimal separator, WebVTT uses '.'.
	secField := strings.ReplaceAll(parts[secIdx+1], ",", ".")
	frac := strings.Split(secField, ".")
	if len(frac) != 2 {
		return 0, errs.New(errs.KindInvalidInput, "subtitle", "bad seconds in %q", raw)
	}
	sec, err := strconv.Atoi(frac[0])
	if err != nil || sec < 0 || sec > 59 {
		return 0, errs.New(errs.KindInvalidInput, "subtitle", "bad seconds in %q", raw)
	}
	fs := frac[1]
	if len(fs) != 3 {
		return 0, errs.New(errs.KindInvalidInput, "subtitle", "milliseconds must be 3 digits in %q", raw)
	}
	ms, err := strconv.Atoi(fs)
	if err != nil || ms < 0 {
		return 0, errs.New(errs.KindInvalidInput, "subtitle", "bad fraction in %q", raw)
	}
	total := int64(h)*3600_000_000_000 + int64(m)*60_000_000_000 +
		int64(sec)*1_000_000_000 + int64(ms)*1_000_000
	return clock.Nanoseconds(total), nil
}

// splitTimestamps splits "start --> end" and returns both clocks.
func splitTimestamps(line, arrow string) (clock.Ticks, clock.Ticks, error) {
	idx := strings.Index(line, arrow)
	if idx < 0 {
		return 0, 0, errs.New(errs.KindInvalidInput, "subtitle", "missing %q arrow", arrow)
	}
	start, err := parseClock(line[:idx])
	if err != nil {
		return 0, 0, errs.New(errs.KindInvalidInput, "subtitle", "start clock: %v", err)
	}
	tail := strings.TrimSpace(line[idx+len(arrow):])
	// WebVTT cue settings may follow the end clock.
	if sp := strings.IndexByte(tail, ' '); sp >= 0 {
		tail = tail[:sp]
	}
	end, err := parseClock(tail)
	if err != nil {
		return 0, 0, errs.New(errs.KindInvalidInput, "subtitle", "end clock: %v", err)
	}
	return start, end, nil
}

// readLines returns trimmed lines with their 1-based source line numbers.
func readLines(r io.Reader) ([]numbered, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	var out []numbered
	n := 0
	for sc.Scan() {
		n++
		out = append(out, numbered{line: n, text: strings.TrimSpace(sc.Text())})
	}
	return out, sc.Err()
}

type numbered struct {
	line int
	text string
}

// finalize validates one assembled cue and appends it.
func finalize(cues *[]Cue, idx int, firstRow int, start, end clock.Ticks, lines []string, c *counter) error {
	if len(lines) == 0 {
		return errs.New(errs.KindInvalidInput, "subtitle", "line %d: empty cue body", firstRow)
	}
	if end <= start {
		return errs.New(errs.KindInvalidInput, "subtitle",
			"line %d: non-positive duration %d..%d", firstRow, start.Ns(), end.Ns())
	}
	size := 0
	for _, l := range lines {
		size += len(l) + 1
	}
	if size > c.cfg.MaxTextBytes {
		return errs.New(errs.KindLimitExceeded, "subtitle",
			"line %d: cue text %d bytes exceeds %d", firstRow, size, c.cfg.MaxTextBytes)
	}
	for _, l := range lines {
		if err := c.add(l); err != nil {
			return err
		}
	}
	*cues = append(*cues, Cue{Index: idx, Start: start, End: end, Lines: lines, FirstRow: firstRow})
	return nil
}
