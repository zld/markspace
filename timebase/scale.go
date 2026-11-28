package timebase

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"mediarc/model"
)

// NanosFromScale converts a media-clock value (for example an MP4 sample time
// or an RTP timestamp) into nanoseconds. Negative values are supported and round
// symmetrically.
func NanosFromScale(value int64, timescale uint32) (int64, error) {
	if timescale == 0 {
		return 0, model.New(model.CodeInvalidArgument, stream, "timescale must be positive")
	}
	neg := value < 0
	magnitude := value
	if neg {
		if value == math.MinInt64 {
			return 0, model.New(model.CodeTimestampOverflow, stream,
				fmt.Sprintf("value %d cannot be negated", value))
		}
		magnitude = -value
	}
	if magnitude > math.MaxInt64/billion {
		return 0, model.New(model.CodeTimestampOverflow, stream,
			fmt.Sprintf("value %d overflows the nanosecond timeline", value))
	}
	num := magnitude * billion
	scale := int64(timescale)
	q, rem := num/scale, num%scale
	if rem != 0 && rem >= (scale+1)/2 {
		q++
	}
	if neg {
		q = -q
	}
	return q, nil
}

// NanosToScale converts nanoseconds back into ticks of the given timescale.
func NanosToScale(ns int64, timescale uint32) (int64, error) {
	if timescale == 0 {
		return 0, model.New(model.CodeInvalidArgument, stream, "timescale must be positive")
	}
	if ns < 0 {
		return 0, model.New(model.CodeNegativeTime, stream, "timestamp must not be negative")
	}
	scale := int64(timescale)
	if ns != 0 && scale > math.MaxInt64/ns {
		return 0, model.New(model.CodeTimestampOverflow, stream, "nanoseconds multiplied by timescale overflow int64")
	}
	return ns * scale / billion, nil
}

// ParseClock parses a subtitle clock ("HH:MM:SS,mmm", "HH:MM:SS.mmm",
// "MM:SS.mmm" or "SS.mmm") into nanoseconds. Separators ',' and '.' are both
// accepted for the fractional part.
func ParseClock(text string) (int64, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0, model.New(model.CodeInvalidTimecode, stream, "empty clock value")
	}
	if strings.HasPrefix(trimmed, "-") {
		return 0, model.New(model.CodeNegativeTime, stream, "clock value must not be negative")
	}
	main, frac, hasFrac := strings.Cut(strings.ReplaceAll(trimmed, ",", "."), ".")
	parts := strings.Split(main, ":")
	if len(parts) < 1 || len(parts) > 3 {
		return 0, model.New(model.CodeInvalidTimecode, stream, fmt.Sprintf("clock %q has too many fields", text))
	}
	values := make([]int64, len(parts))
	for i, part := range parts {
		if part == "" {
			return 0, model.New(model.CodeInvalidTimecode, stream, fmt.Sprintf("clock %q has an empty field", text))
		}
		v, err := strconv.ParseInt(part, 10, 64)
		if err != nil || v < 0 {
			return 0, model.New(model.CodeInvalidTimecode, stream, fmt.Sprintf("clock %q field %q is not a number", text, part))
		}
		values[i] = v
	}
	var hours, minutes, seconds int64
	switch len(values) {
	case 3:
		hours, minutes, seconds = values[0], values[1], values[2]
	case 2:
		minutes, seconds = values[0], values[1]
	case 1:
		seconds = values[0]
	}
	if minutes >= 60 || seconds >= 60 {
		return 0, model.New(model.CodeInvalidTimecode, stream,
			fmt.Sprintf("clock %q has out-of-range minutes or seconds", text))
	}
	if hasFrac {
		if len(frac) == 0 || len(frac) > 9 {
			return 0, model.New(model.CodeInvalidTimecode, stream,
				fmt.Sprintf("clock %q fraction must have 1-9 digits", text))
		}
		for _, ch := range frac {
			if ch < '0' || ch > '9' {
				return 0, model.New(model.CodeInvalidTimecode, stream,
					fmt.Sprintf("clock %q fraction is not numeric", text))
			}
		}
	}
	hourSeconds, err := checkedMul(hours, 3600)
	if err != nil {
		return 0, err
	}
	secondsCollapsed, err := checkedAdd(hourSeconds, minutes*60)
	if err != nil {
		return 0, err
	}
	secondsCollapsed, err = checkedAdd(secondsCollapsed, seconds)
	if err != nil {
		return 0, err
	}
	ns, err := checkedMul(secondsCollapsed, billion)
	if err != nil {
		return 0, err
	}
	if !hasFrac || frac == "" {
		return ns, nil
	}
	fraction := frac + strings.Repeat("0", 9-len(frac))
	part, err := strconv.ParseInt(fraction, 10, 64)
	if err != nil {
		return 0, model.New(model.CodeInvalidTimecode, stream, fmt.Sprintf("clock %q fraction is invalid", text))
	}
	return checkedAdd(ns, part)
}

// FormatNanos renders ns as "HH:MM:SS.mmm" for reports.
func FormatNanos(ns int64) string {
	if ns < 0 {
		return "-" + FormatNanos(-ns)
	}
	hours := ns / (3600 * billion)
	ns %= 3600 * billion
	minutes := ns / (60 * billion)
	ns %= 60 * billion
	seconds := ns / billion
	millis := (ns % billion) / 1_000_000
	return fmt.Sprintf("%02d:%02d:%02d.%03d", hours, minutes, seconds, millis)
}
