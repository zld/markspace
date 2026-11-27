// Package hls parses M3U8 master and media playlists. Discontinuity markers
// are captured explicitly so downstream clock recovery can reset the carrier
// timeline instead of treating the jump as drift or wraparound.
package hls

import (
	"strconv"
	"strings"

	"mediarc/internal/model"
)

// Entry is one media playlist segment with its source line numbers.
type Entry struct {
	Segment       model.SegmentRef
	Discontinuity bool // true when EXT-X-DISCONTINUITY precedes this entry
}

// Playlist is the parsed result of one media playlist.
type Playlist struct {
	URL           string
	MediaSequence int
	TargetDurSec  float64
	Entries       []Entry
	Errors        []*model.MediaError
	TotalMs       int64
}

// Variant is one entry of a master playlist.
type Variant struct {
	URL       string
	Bandwidth int
	Line      int
}

// ParseMedia parses a media (variant) playlist.
func ParseMedia(fileName string, data []byte) (Playlist, error) {
	pl := Playlist{MediaSequence: 0}
	lines := strings.Split(string(data), "\n")
	seenURL := map[string]int{}
	pendingDiscontinuity := false
	pendingDur := int64(-1)
	var cursorMs int64
	opened := false

	for idx, raw := range lines {
		lineNo := idx + 1
		line := strings.TrimSpace(raw)
		if line == "" || (strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "#EXT")) {
			continue
		}
		switch {
		case line == "#EXTM3U":
			opened = true
		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			if v, err := strconv.ParseFloat(value(line), 64); err == nil {
				pl.TargetDurSec = v
			}
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			if v, err := strconv.Atoi(value(line)); err == nil {
				pl.MediaSequence = v
			}
		case line == "#EXT-X-DISCONTINUITY":
			pendingDiscontinuity = true
		case strings.HasPrefix(line, "#EXTINF:"):
			secs, err := strconv.ParseFloat(strings.TrimSuffix(value(line), ","), 64)
			if err != nil || secs <= 0 {
				pl.Errors = append(pl.Errors, model.AtLine(model.KindMissingDuration, fileName, lineNo,
					"unparsable EXTINF duration: "+value(line)))
				pendingDur = -1
				continue
			}
			pendingDur = int64(secs*1000 + 0.5)
		case strings.HasPrefix(line, "#EXT-X-"):
			if !isToleratedTag(line) {
				pl.Errors = append(pl.Errors, model.AtLine(model.KindManifestSyntax, fileName, lineNo,
					"unparsable tag: "+line))
			}
		case strings.HasPrefix(line, "#"):
			pl.Errors = append(pl.Errors, model.AtLine(model.KindManifestSyntax, fileName, lineNo,
				"unknown comment-style tag: "+line))
		default:
			// Media URI line.
			if !opened {
				return pl, model.AtLine(model.KindManifestSyntax, fileName, lineNo,
					"playlist does not start with #EXTM3U")
			}
			if pendingDur < 0 {
				pl.Errors = append(pl.Errors, model.AtLine(model.KindMissingDuration, fileName, lineNo,
					"segment URI without preceding EXTINF: "+line))
			}
			if firstLine, dup := seenURL[line]; dup {
				pl.Errors = append(pl.Errors, model.AtLine(model.KindDuplicateReference, fileName, lineNo,
					"segment "+line+" already referenced at line "+strconv.Itoa(firstLine)))
			} else {
				seenURL[line] = lineNo
				dur := pendingDur
				if dur < 0 {
					dur = 0
				}
				pl.Entries = append(pl.Entries, Entry{
					Segment: model.SegmentRef{
						URL: line, Index: len(pl.Entries), Line: lineNo,
						StartMs: cursorMs, DurationMs: dur,
					},
					Discontinuity: pendingDiscontinuity,
				})
				cursorMs += dur
				pl.TotalMs = cursorMs
			}
			pendingDur = -1
			pendingDiscontinuity = false
		}
	}

	if !opened {
		return pl, model.AtLine(model.KindManifestTruncated, fileName, len(lines),
			"#EXTM3U header missing; playlist truncated")
	}
	if pl.TargetDurSec > 0 {
		limit := int64(pl.TargetDurSec*1000+0.5) * int64(len(pl.Entries)+1)
		for _, e := range pl.Entries {
			if e.Segment.StartMs+e.Segment.DurationMs > limit {
				pl.Errors = append(pl.Errors, model.AtLine(model.KindRangeOutOfBounds, fileName, e.Segment.Line,
					"segment range exceeds target-duration budget"))
			}
		}
	}
	return pl, nil
}

// ParseMaster extracts variant playlist references.
func ParseMaster(fileName string, data []byte) ([]Variant, []*model.MediaError, error) {
	lines := strings.Split(string(data), "\n")
	var variants []Variant
	var errs []*model.MediaError
	opened := false
	var pendingBw int
	for idx, raw := range lines {
		lineNo := idx + 1
		line := strings.TrimSpace(raw)
		switch {
		case line == "#EXTM3U":
			opened = true
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			for _, attr := range strings.Split(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"), ",") {
				kv := strings.SplitN(attr, "=", 2)
				if len(kv) == 2 && strings.TrimSpace(kv[0]) == "BANDWIDTH" {
					bw, err := strconv.Atoi(strings.Trim(kv[1], "\""))
					if err != nil {
						errs = append(errs, model.AtLine(model.KindManifestSyntax, fileName, lineNo, "bad BANDWIDTH"))
						continue
					}
					pendingBw = bw
				}
			}
		case strings.HasPrefix(line, "#") || line == "":
			continue
		default:
			if !opened {
				return nil, nil, model.AtLine(model.KindManifestTruncated, fileName, lineNo, "master playlist missing #EXTM3U")
			}
			variants = append(variants, Variant{URL: line, Bandwidth: pendingBw, Line: lineNo})
			pendingBw = 0
		}
	}
	return variants, errs, nil
}

// DiscontinuityIndexes returns segment indexes carrying a discontinuity.
func (p Playlist) DiscontinuityIndexes() []int {
	var out []int
	for _, e := range p.Entries {
		if e.Discontinuity {
			out = append(out, e.Segment.Index)
		}
	}
	return out
}

func value(line string) string {
	i := strings.IndexByte(line, ':')
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(line[i+1:])
}

func isToleratedTag(line string) bool {
	switch {
	case strings.HasPrefix(line, "#EXT-X-KEY"),
		strings.HasPrefix(line, "#EXT-X-MAP"),
		strings.HasPrefix(line, "#EXT-X-PROGRAM-DATE-TIME"),
		strings.HasPrefix(line, "#EXT-X-ENDLIST"),
		strings.HasPrefix(line, "#EXT-X-VERSION"),
		strings.HasPrefix(line, "#EXT-X-PLAYLIST-TYPE"),
		strings.HasPrefix(line, "#EXT-X-ALLOW-CACHE"),
		strings.HasPrefix(line, "#EXT-X-INDEPENDENT-SEGMENTS"):
		return true
	}
	return false
}
