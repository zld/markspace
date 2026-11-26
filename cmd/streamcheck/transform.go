package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"streamcheck/manifest"
)

// normalizedTimeline is the v2 migration output format.
type normalizedTimeline struct {
	Format          string              `json:"format"`
	Segments        int                 `json:"segments"`
	Intervals       int                 `json:"intervals"`
	Discontinuities int                 `json:"discontinuities"`
	Timeline        []normalizedSegment `json:"timeline"`
}

type normalizedSegment struct {
	Index         int    `json:"index"`
	StartMs       int64  `json:"startMs"`
	DurMs         int64  `json:"durMs"`
	URI           string `json:"uri"`
	Discontinuity bool   `json:"discontinuity"`
	Interval      int    `json:"interval"`
}

// transformLegacy upgrades a v1 text manifest to the normalized v2 JSON
// timeline, reusing the HLS parser and continuity-interval analysis.
func transformLegacy(old []byte) ([]byte, error) {
	lines := strings.Split(strings.TrimSpace(string(old)), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "STREAMCHECK-LEGACY-V1" {
		return nil, errors.New("not a legacy v1 manifest")
	}
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n")
	segCount := 0
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || line == "END":
			// terminator
		case line == "DISCONTINUITY":
			b.WriteString("#EXT-X-DISCONTINUITY\n")
		case strings.HasPrefix(line, "SEG "):
			var start, dur int64
			var uri string
			if _, err := fmt.Sscanf(line, "SEG %d %d %s", &start, &dur, &uri); err != nil {
				return nil, fmt.Errorf("bad SEG line %q: %w", line, err)
			}
			fmt.Fprintf(&b, "#EXTINF:%d.%03d,\n%s\n", dur/1000, dur%1000, uri)
			segCount++
		default:
			return nil, fmt.Errorf("unknown legacy directive %q", line)
		}
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	if segCount == 0 {
		return nil, errors.New("legacy manifest has no segments")
	}
	tl, err := manifest.ParseHLS([]byte(b.String()), "legacy")
	if err != nil {
		return nil, err
	}
	tl.Analyze()
	rep := tl.Reps[0]
	out := normalizedTimeline{
		Format:          "streamcheck-normalized-v2",
		Segments:        len(rep.Segments),
		Intervals:       rep.Intervals(),
		Discontinuities: rep.Discontinuities(),
	}
	for _, s := range rep.Segments {
		out.Timeline = append(out.Timeline, normalizedSegment{
			Index:         s.Index,
			StartMs:       s.Start.Millis(),
			DurMs:         s.Dur.Millis(),
			URI:           s.URI,
			Discontinuity: s.Discontinuity,
			Interval:      s.Interval,
		})
	}
	return json.MarshalIndent(out, "", "  ")
}

// validateNormalized is the shadow-validation hook for migration output.
func validateNormalized(data []byte) error {
	var nt normalizedTimeline
	if err := json.Unmarshal(data, &nt); err != nil {
		return fmt.Errorf("output is not normalized JSON: %w", err)
	}
	if nt.Format != "streamcheck-normalized-v2" {
		return errors.New("bad format tag")
	}
	if nt.Segments == 0 || nt.Segments != len(nt.Timeline) {
		return errors.New("segment count mismatch")
	}
	if nt.Intervals < 1 {
		return errors.New("no continuity intervals")
	}
	return nil
}
