package main

import (
	"strconv"
	"strings"

	"rtpreplay/media"
)

// parseBitrates parses the --bitrates CSV, rejecting empty and non-positive
// values. Order is preserved because representations are processed in it.
func parseBitrates(csv string) ([]int, *media.LocatableError) {
	var out []int
	seen := map[int]bool{}
	for _, part := range strings.Split(csv, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		v, err := strconv.Atoi(part)
		if err != nil {
			return nil, media.NewError(media.KindBadInput, "BAD_BITRATE", "regress", -1,
				"%q is not an integer", part)
		}
		if v <= 0 {
			return nil, media.NewError(media.KindBadInput, "BAD_BITRATE", "regress", -1,
				"bitrate must be positive, got %d", v)
		}
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, media.NewError(media.KindBadInput, "NO_BITRATES", "regress", -1,
			"at least one bitrate is required")
	}
	return out, nil
}
