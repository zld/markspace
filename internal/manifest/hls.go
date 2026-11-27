// Package manifest lifts HLS and DASH manifest references into segment
// events that keep their source location (manifest path and line).
package manifest

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mediacheck/internal/timeline"
)

// Event is one segment reference lifted from a manifest. Raw times stay in
// the manifest's own timescale; conversion happens in the timeline stage.
type Event struct {
	ManifestPath   string `json:"manifest_path"`
	Format         string `json:"format"` // "hls" or "dash"
	Representation string `json:"representation"`
	Seq            int64  `json:"seq"`
	RawTime        uint64 `json:"raw_time"`
	RawDuration    uint64 `json:"raw_duration"`
	Timescale      uint64 `json:"timescale"`
	Discontinuity  bool   `json:"discontinuity"`
	SegmentPath    string `json:"segment_path"`
	Line           int    `json:"line"`
}

// hlsTimescale: EXTINF seconds are parsed into microseconds.
const hlsTimescale = 1_000_000

// ParseHLS reads an HLS media playlist into segment events, resolving media
// sequence numbers, EXTINF durations and discontinuity markers.
func ParseHLS(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, timeline.New(timeline.KindMissingRecord, "manifest.hls", path, 0, "open: %v", err)
	}
	defer f.Close()

	var events []Event
	var seq, cur uint64
	discontinuity := false
	var pendingDur uint64
	durSeen := false
	durLine := 0

	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			v := strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:")
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				return nil, timeline.New(timeline.KindBadSequence, "manifest.hls", path, lineNo,
					"invalid media sequence %q", v)
			}
			seq = uint64(n)
		case line == "#EXT-X-DISCONTINUITY":
			discontinuity = true
		case strings.HasPrefix(line, "#EXTINF:"):
			v := strings.TrimPrefix(line, "#EXTINF:")
			if i := strings.IndexByte(v, ','); i >= 0 {
				v = v[:i]
			}
			d, err := parseSeconds(v)
			if err != nil || d == 0 {
				return nil, timeline.New(timeline.KindMissingDuration, "manifest.hls", path, lineNo,
					"invalid EXTINF duration %q", v)
			}
			pendingDur, durSeen, durLine = d, true, lineNo
		case line == "" || strings.HasPrefix(line, "#"):
			// blank lines and other tags carry no segment reference
		default:
			if !durSeen {
				return nil, timeline.New(timeline.KindMissingDuration, "manifest.hls", path, lineNo,
					"segment %q has no preceding EXTINF", line)
			}
			events = append(events, Event{
				ManifestPath: path, Format: "hls",
				Representation: filepath.Base(path),
				Seq:            int64(seq),
				RawTime:        cur,
				RawDuration:    pendingDur,
				Timescale:      hlsTimescale,
				Discontinuity:  discontinuity,
				SegmentPath:    filepath.Join(filepath.Dir(path), line),
				Line:           durLine,
			})
			seq++
			cur += pendingDur
			discontinuity = false
			durSeen = false
		}
	}
	if err := sc.Err(); err != nil {
		return nil, timeline.New(timeline.KindMissingRecord, "manifest.hls", path, lineNo, "read: %v", err)
	}
	return events, nil
}

// parseSeconds parses a decimal seconds value ("1.5") into microseconds.
func parseSeconds(s string) (uint64, error) {
	whole, frac, _ := strings.Cut(s, ".")
	w, err := strconv.ParseUint(whole, 10, 64)
	if err != nil {
		return 0, err
	}
	if len(frac) > 6 {
		frac = frac[:6]
	}
	for len(frac) < 6 {
		frac += "0"
	}
	f, err := strconv.ParseUint(frac, 10, 64)
	if err != nil {
		return 0, err
	}
	return w*1_000_000 + f, nil
}
