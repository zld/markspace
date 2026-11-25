package index

import (
	"bufio"
	"os"
	"strconv"
	"strings"

	"mediacore/internal/clock"
)

// LocatedInterval is a test/verification interval with its source byte offset.
type LocatedInterval struct {
	Start  int64 `json:"start_ns"`
	End    int64 `json:"end_ns"`
	Offset int64 `json:"raw_offset"`
}

// LoadIntervalFile reads lines of "start_ns end_ns". It records each line's
// byte offset so an overlap can be reported with a raw offset.
func LoadIntervalFile(path string) ([]LocatedInterval, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, clock.Wrap(clock.CategoryIO, path, 0, 0, "open intervals", err)
	}
	defer f.Close()
	var out []LocatedInterval
	scanner := bufio.NewScanner(f)
	var offset int64
	for scanner.Scan() {
		line := scanner.Text()
		lineLen := int64(len(scanner.Bytes()) + 1)
		trim := strings.TrimSpace(line)
		if trim != "" && !strings.HasPrefix(trim, "#") {
			fields := strings.Fields(trim)
			if len(fields) != 2 {
				return nil, clock.New(clock.CategoryInvalidArgument, path, 0, offset, "interval line must have two integers")
			}
			start, err1 := strconv.ParseInt(fields[0], 10, 64)
			end, err2 := strconv.ParseInt(fields[1], 10, 64)
			if err1 != nil || err2 != nil {
				return nil, clock.New(clock.CategoryInvalidArgument, path, 0, offset, "invalid interval integer")
			}
			out = append(out, LocatedInterval{Start: start, End: end, Offset: offset})
		}
		offset += lineLen
	}
	if err := scanner.Err(); err != nil {
		return nil, clock.Wrap(clock.CategoryIO, path, 0, offset, "read intervals", err)
	}
	return out, ValidateDisjoint(path, out)
}

// ValidateDisjoint rejects overlapping intervals. Intervals that merely touch
// at a boundary are allowed because query ranges are left closed/right open.
func ValidateDisjoint(path string, intervals []LocatedInterval) error {
	for i := 1; i < len(intervals); i++ {
		prev, cur := intervals[i-1], intervals[i]
		if cur.Start < prev.End && prev.Start < cur.End {
			return clock.New(clock.CategoryOverlappingInterval, path, 0, cur.Offset,
				"interval overlaps preceding interval")
		}
	}
	return nil
}
