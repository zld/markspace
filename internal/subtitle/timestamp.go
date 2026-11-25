package subtitle

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	"archivecheck/internal/clock"
)

var timestampPattern = regexp.MustCompile(`^(?:([0-9]+):)?([0-9]{2}):([0-9]{2})[.,]([0-9]{1,9})$`)

// ParseTimestamp parses SRT/VTT clock text into nanoseconds. It accepts both
// HH:MM:SS.mmm and the WebVTT MM:SS.mmm short form.
func ParseTimestamp(token string) (clock.Nanoseconds, error) {
	m := timestampPattern.FindStringSubmatch(token)
	if m == nil {
		return 0, strconv.ErrSyntax
	}

	var hours uint64
	var err error
	if m[1] != "" {
		hours, err = strconv.ParseUint(m[1], 10, 64)
		if err != nil {
			return 0, err
		}
	}
	minutes, err := strconv.ParseUint(m[2], 10, 64)
	if err != nil {
		return 0, err
	}
	seconds, err := strconv.ParseUint(m[3], 10, 64)
	if err != nil {
		return 0, err
	}
	if minutes >= 60 || seconds >= 60 {
		return 0, strconv.ErrRange
	}
	if hours > uint64(math.MaxInt64)/uint64(clock.Hour) {
		return 0, strconv.ErrRange
	}

	frac := m[4] + strings.Repeat("0", 9-len(m[4]))
	fractionNS, err := strconv.ParseUint(frac, 10, 64)
	if err != nil || fractionNS >= uint64(clock.Second) {
		return 0, strconv.ErrRange
	}

	return clock.Nanoseconds(hours)*clock.Hour +
		clock.Nanoseconds(minutes)*clock.Minute +
		clock.Nanoseconds(seconds)*clock.Second +
		clock.Nanoseconds(fractionNS), nil
}
