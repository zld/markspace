// Package events parses the client event log.
//
// Each event is one line of the form
//
//	<timestamp> <kind> [key=value ...] [text...]
//
// The timestamp is either an absolute wall clock value (Unix milliseconds or
// RFC 3339) or a media-relative offset ("+1500ms", "+12000t"). Lines that
// cannot be parsed, whose time overflows, or whose wall clock cannot be tied to
// the capture start are kept as records marked unalignable: one bad event never
// aborts the others.
package events

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
	"time"

	"rtp-rebuild/internal/clock"
)

// Kind is a client event type as written in the log.
type Kind string

// Known event kinds. Unknown kinds are kept verbatim.
const (
	KindStall  Kind = "stall"
	KindFreeze Kind = "freeze"
	KindResume Kind = "resume"
	KindSeek   Kind = "seek"
	KindJoin   Kind = "join"
	KindLeave  Kind = "leave"
	// KindUnknown marks an event whose kind could not be determined.
	KindUnknown Kind = "unknown"
)

// Event is one parsed client event.
type Event struct {
	// WallMillis is the absolute wall clock of the event in Unix milliseconds,
	// or 0 when the event only carries a relative offset.
	WallMillis int64
	// Relative is the media-relative offset when the log stated one.
	Relative    clock.Tick
	HasRelative bool

	Kind  Kind
	Attrs map[string]string
	Raw   string
	Line  int
	// MediaTime is filled by the align stage. Alignable reports whether it
	// could be computed.
	MediaTime clock.Tick
	Alignable bool
	// Reason explains why an event is not alignable.
	Reason string
}

// Log is the parsed event log.
type Log struct {
	Events []Event
	// Unalignable counts events kept but excluded from the media timeline.
	Unalignable int
}

// Parse reads every event line. Only an I/O style failure aborts parsing; line
// level problems become unalignable events.
func Parse(data []byte) *Log {
	log := &Log{}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		ev := parseLine(raw, line)
		if ev.Reason != "" {
			log.Unalignable++
		}
		log.Events = append(log.Events, ev)
	}
	return log
}

func parseLine(raw string, line int) Event {
	ev := Event{Kind: KindUnknown, Attrs: map[string]string{}, Raw: raw, Line: line}
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		ev.Reason = "empty event"
		return ev
	}
	tsStr := fields[0]
	rest := fields[1:]

	if strings.HasPrefix(tsStr, "ts=") {
		tsStr = strings.TrimPrefix(tsStr, "ts=")
		if len(rest) > 0 {
			ev.Kind = Kind(rest[0])
			rest = rest[1:]
		}
	} else {
		if len(rest) == 0 {
			ev.Reason = "missing event kind"
			return ev
		}
		ev.Kind = Kind(rest[0])
		rest = rest[1:]
	}
	if ev.Kind == "" {
		ev.Kind = KindUnknown
	}

	for _, f := range rest {
		if k, v, ok := strings.Cut(f, "="); ok {
			ev.Attrs[k] = v
			continue
		}
		if ev.Attrs["text"] == "" {
			ev.Attrs["text"] = f
		} else {
			ev.Attrs["text"] += " " + f
		}
	}

	switch {
	case strings.HasPrefix(tsStr, "+"):
		tick, err := parseRelative(tsStr[1:])
		if err != nil {
			ev.Reason = err.Error()
			return ev
		}
		ev.Relative = tick
		ev.HasRelative = true
	default:
		ms, err := parseWall(tsStr)
		if err != nil {
			ev.Reason = err.Error()
			return ev
		}
		ev.WallMillis = ms
	}
	return ev
}

// parseRelative accepts "1500ms", "1500", "12000t" (ticks) and "1.5s".
func parseRelative(s string) (clock.Tick, error) {
	switch {
	case strings.HasSuffix(s, "ms"):
		ms, err := strconv.ParseInt(strings.TrimSuffix(s, "ms"), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("relative time %q: %w", s, err)
		}
		return clock.FromMillis(ms)
	case strings.HasSuffix(s, "t"):
		t, err := strconv.ParseInt(strings.TrimSuffix(s, "t"), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("relative time %q: %w", s, err)
		}
		if err := clock.Tick(t).Validate(); err != nil {
			return 0, err
		}
		return clock.Tick(t), nil
	case strings.HasSuffix(s, "s"):
		secs, err := strconv.ParseFloat(strings.TrimSuffix(s, "s"), 64)
		if err != nil {
			return 0, fmt.Errorf("relative time %q: %w", s, err)
		}
		return clock.FromDuration(time.Duration(secs * float64(time.Second)))
	default:
		ms, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("relative time %q: %w", s, err)
		}
		return clock.FromMillis(ms)
	}
}

// parseWall accepts Unix milliseconds, Unix seconds with a decimal part, and
// RFC 3339 timestamps.
func parseWall(s string) (int64, error) {
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		// Heuristic: values below 1e11 are seconds (year < 5138 in ms would be
		// ~1973), which is unambiguous for this dataset.
		if v < 1_000_000_000_000 {
			return v * 1000, nil
		}
		return v, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UnixMilli(), nil
	}
	return 0, fmt.Errorf("event time %q is not a recognised timestamp", s)
}

// LogOffsetKey is the attribute that records a per-event unknown clock offset.
const LogOffsetKey = "clock_offset"

// IsAlignable reports whether the event can be placed on the media timeline
// given the capture start wall clock. A zero start means the capture did not
// record a usable wall clock, in which case only relative events align.
func (e *Event) IsAlignable(captureStartMillis int64, hasCaptureStart bool) bool {
	if e.HasRelative {
		return true
	}
	if e.WallMillis == 0 {
		return false
	}
	return hasCaptureStart
}

// MediaFromWall converts the event wall clock to media ticks.
func MediaFromWall(wallMillis, captureStartMillis int64) (clock.Tick, error) {
	deltaMillis, err := subInt64(wallMillis, captureStartMillis)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", clock.ErrOverflow, err)
	}
	return clock.FromMillis(deltaMillis)
}

func subInt64(a, b int64) (int64, error) {
	d := a - b
	if (b > 0 && d > a) || (b < 0 && d < a) {
		return 0, fmt.Errorf("subtraction overflow: %d - %d", a, b)
	}
	return d, nil
}
