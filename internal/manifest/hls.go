package manifest

import (
	"bufio"
	"io"
	"strconv"
	"strings"

	"mediakernel/internal/clock"
	"mediakernel/internal/mediaerr"
)

// HLSSegment is one media segment with its sequence ordinal.
type HLSSegment struct {
	Sequence     uint64 `json:"sequence"`
	URL          string `json:"url"`
	DurationTick uint64 `json:"duration_tick"`
	StartTick    uint64 `json:"start_tick"`
	Group        int    `json:"group"` // contiguous region between discontinuities
}

// HLSDoc is the validated HLS parse result.
type HLSDoc struct {
	TargetDurationTick uint64            `json:"target_duration_tick"`
	Groups             [][]HLSSegment    `json:"groups"`
	Isolated           []GapWindow       `json:"isolated"` // regions cut around breaks
	Errors             []*mediaerr.Error `json:"errors"`
}

// GapWindow marks an isolated time range in master ticks.
type GapWindow struct {
	StartTick uint64 `json:"start_tick"`
	EndTick   uint64 `json:"end_tick"`
	Reason    string `json:"reason"`
}

// ParseHLS parses a media playlist. EXT-X-DISCONTINUITY splits the timeline
// into groups; sequence rollback or missing references isolate the affected
// region and are reported, never spliced across the break.
func ParseHLS(r io.Reader) (*HLSDoc, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	doc := &HLSDoc{}
	var (
		mediaSeq    uint64
		haveSeq     bool
		pendingDisc bool
		group       = -1
		cursor      uint64
		current     []HLSSegment
		pendingDur  uint64
		haveDur     bool
		lastSrcSeq  uint64 // source sequence parsed from the previous URL
		seenSegURL  = map[string]bool{}
	)

	flush := func() {
		if len(current) > 0 {
			doc.Groups = append(doc.Groups, current)
			current = nil
		}
	}

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "#") {
			if !haveDur {
				return nil, mediaerr.New(mediaerr.CatManifest, "hls_duration",
					"segment without preceding EXTINF").With(line, 0, false)
			}
			if seenSegURL[line] {
				return nil, mediaerr.New(mediaerr.CatManifest, "duplicate_segment",
					"duplicate HLS segment reference "+line).With(line, 0, false)
			}
			seenSegURL[line] = true

			// Sequence numbers come from the trailing digits of each
			// reference (deterministic project naming, e.g. seg007.ts -> 7).
			// EXT-X-MEDIA-SEQUENCE only anchors the very first segment.
			srcSeq := trailingSequence(line)
			if haveSeq && group == -1 && len(current) == 0 {
				srcSeq = mediaSeq
			}
			if group == -1 || pendingDisc {
				// First group, or an explicit discontinuity: start a fresh region.
				flush()
				group++
				cursor = 0
			} else if len(current) > 0 {
				switch {
				case srcSeq < lastSrcSeq:
					doc.Isolated = append(doc.Isolated, GapWindow{
						StartTick: cursor, EndTick: cursor, Reason: "sequence_rollback"})
					doc.Errors = append(doc.Errors, mediaerr.New(
						mediaerr.CatDiscontinuity, "rollback",
						"media sequence moved backwards; region isolated").
						With(line, cursor, false))
					flush()
					group++
					cursor = 0
				case srcSeq >= lastSrcSeq+2:
					doc.Isolated = append(doc.Isolated, GapWindow{
						StartTick: cursor, EndTick: cursor, Reason: "missing_reference"})
					doc.Errors = append(doc.Errors, mediaerr.New(
						mediaerr.CatDiscontinuity, "missing_ref",
						"missing segment reference in sequence; region isolated").
						With(line, cursor, true))
					flush()
					group++
					cursor = 0
				}
			}
			seg := HLSSegment{
				Sequence: srcSeq, URL: line,
				DurationTick: pendingDur, StartTick: cursor, Group: group,
			}
			current = append(current, seg)
			lastSrcSeq = srcSeq
			cursor, _ = clock.Add(cursor, pendingDur)
			if haveSeq {
				mediaSeq++
			}
			pendingDisc, haveDur, pendingDur = false, false, 0
			if len(current) > mediaerr.MaxSegments {
				return nil, mediaerr.New(mediaerr.CatValidation, "segment_cap",
					"HLS segment count exceeds limit")
			}
			continue
		}
		tag, value, _ := strings.Cut(line, ":")
		switch tag {
		case "#EXT-X-MEDIA-SEQUENCE":
			v, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
			if err != nil {
				return nil, mediaerr.New(mediaerr.CatManifest, "hls_seq", "bad media sequence")
			}
			if haveSeq && v != mediaSeq {
				return nil, mediaerr.New(mediaerr.CatDiscontinuity, "seq_tag_change",
					"media sequence tag changed mid-playlist").With(value, 0, false)
			}
			mediaSeq, haveSeq = v, true
		case "#EXT-X-TARGETDURATION":
			secs, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
			if err != nil {
				return nil, mediaerr.New(mediaerr.CatManifest, "hls_target", "bad target duration")
			}
			doc.TargetDurationTick, _ = clock.FromMillis(secs * 1000)
		case "#EXTINF":
			secs, err := parseEXTINF(value)
			if err != nil {
				return nil, mediaerr.New(mediaerr.CatManifest, "hls_extinf", err.Error())
			}
			pendingDur = secs
			haveDur = true
		case "#EXT-X-DISCONTINUITY":
			pendingDisc = true
		case "#EXT-X-ENDLIST", "#EXTM3U", "#EXT-X-VERSION",
			"#EXT-X-PLAYLIST-TYPE", "#EXT-X-INDEPENDENT-SEGMENTS":
			// structural tags we accept and ignore
		default:
			if strings.HasPrefix(tag, "#EXT-X-KEY") {
				return nil, mediaerr.New(mediaerr.CatValidation, "encrypted",
					"encrypted HLS playlists are not supported")
			}
			// unknown tags are tolerated to stay forward compatible
		}
	}
	if err := sc.Err(); err != nil {
		return nil, mediaerr.New(mediaerr.CatManifest, "read", "playlist read failure").Wrap(err)
	}
	flush()
	if group == -1 {
		return nil, mediaerr.New(mediaerr.CatManifest, "empty_playlist", "playlist has no segments")
	}
	return doc, nil
}

// trailingSequence extracts the trailing decimal run of a reference name
// (e.g. "seg007.ts" -> 7). Names without digits fall back to 0, in which case
// continuity is driven solely by the listing order and EXT-X-MEDIA-SEQUENCE.
func trailingSequence(url string) uint64 {
	base := url
	if dot := strings.LastIndexByte(url, '.'); dot >= 0 {
		base = url[:dot]
	}
	end := len(base)
	start := end
	for start > 0 && base[start-1] >= '0' && base[start-1] <= '9' {
		start--
	}
	if start == end {
		return 0
	}
	v, err := strconv.ParseUint(base[start:end], 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// parseEXTINF accepts decimal seconds (comma-separated title ignored).
func parseEXTINF(v string) (uint64, error) {
	secPart, _, _ := strings.Cut(v, ",")
	secPart = strings.TrimSpace(secPart)
	if secPart == "" {
		return 0, mediaerr.ErrMalformed
	}
	if dot := strings.IndexByte(secPart, '.'); dot >= 0 {
		intPart := secPart[:dot]
		frac := secPart[dot+1:]
		if len(frac) > 3 {
			frac = frac[:3]
		}
		for len(frac) < 3 {
			frac += "0"
		}
		secs, err1 := strconv.ParseUint(intPart, 10, 64)
		ms, err2 := strconv.ParseUint(frac, 10, 64)
		if err1 != nil || err2 != nil {
			return 0, mediaerr.ErrMalformed
		}
		return clock.FromMillis(secs*1000 + ms)
	}
	secs, err := strconv.ParseUint(secPart, 10, 64)
	if err != nil {
		return 0, mediaerr.ErrMalformed
	}
	return clock.FromMillis(secs * 1000)
}
