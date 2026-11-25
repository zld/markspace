package main

import (
	"strconv"
	"strings"

	"rtpreplay/media"
)

// parseBitrateCSV parses a comma-separated positive-integer list, preserving
// order and dropping duplicates.
func parseBitrateCSV(csv string) ([]int, error) {
	var out []int
	seen := map[int]bool{}
	for _, part := range strings.Split(csv, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		v, err := strconv.Atoi(part)
		if err != nil || v <= 0 {
			return nil, media.NewError(media.KindBadInput, "BAD_BITRATE", "fixture", -1,
				"invalid bitrate %q", part)
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil, media.NewError(media.KindBadInput, "NO_BITRATES", "fixture", -1, "no bitrates")
	}
	return out, nil
}
