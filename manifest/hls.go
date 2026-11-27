package manifest

import (
	"bufio"
	"io"
	"math"
	"strconv"
	"strings"
)

// ParseHLS parses an HLS media playlist from r. baseDir is prepended to every
// (validated) relative segment URI at join time, not inside the model.
func ParseHLS(r io.Reader, programKey, baseDir string) (*Manifest, error) {
	asset := programKey + "#hls"
	m := &Manifest{ProgramKey: programKey, Format: "hls", BaseDir: baseDir}

	trackID := KindVideo
	track := m.findTrack(trackID, true)
	track.Kind = KindVideo
	track.Timescale = hlsTimescale

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	line := 0
	var pendingDur float64
	var havePending bool
	var seq int64
	var mediaSeq int64
	var sawDiscontinuity bool
	var cursorNS int64

	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		if !strings.HasPrefix(raw, "#") {
			if !SafeURI(raw) {
				return nil, &Error{Code: ErrPathTraversal, Asset: asset, Track: trackID,
					Stage: "hls.uri", Line: line, Message: "illegal segment uri: " + raw}
			}
			if !havePending {
				return nil, &Error{Code: ErrSyntax, Asset: asset, Track: trackID,
					Stage: "hls.segment", Line: line, Message: "segment without EXTINF duration"}
			}
			durNS, err := floatDurToNS(pendingDur, asset, trackID, "hls.extinf", line)
			if err != nil {
				return nil, err
			}
			if err := checkNSRange(cursorNS, durNS, asset, trackID, "hls.timeline", line); err != nil {
				return nil, err
			}
			track.Segments = append(track.Segments, SegmentRef{
				Index:      len(track.Segments),
				URI:        raw,
				StartNS:    cursorNS,
				DurationNS: durNS,
				Sequence:   seq,
			})
			cursorNS += durNS
			seq++
			havePending = false
			continue
		}
		tag, val := splitTag(raw)
		switch tag {
		case "EXT-X-TARGETDURATION":
			// informational; no action needed
		case "EXT-X-MEDIA-SEQUENCE":
			n, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
			if err != nil || n < 0 {
				return nil, &Error{Code: ErrSyntax, Asset: asset, Track: trackID,
					Stage: "hls.media_sequence", Line: line, Message: "bad media sequence: " + val}
			}
			mediaSeq = n
			seq = n
		case "EXTINF":
			d, err := parseEXTINF(val)
			if err != nil {
				return nil, &Error{Code: ErrBadDuration, Asset: asset, Track: trackID,
					Stage: "hls.extinf", Line: line, Message: err.Error()}
			}
			pendingDur, havePending = d, true
		case "EXT-X-DISCONTINUITY":
			sawDiscontinuity = true
		case "EXT-X-MAP":
			uri, perr := hlsAttrURI(val)
			if perr != nil || !SafeURI(uri) {
				return nil, &Error{Code: ErrPathTraversal, Asset: asset, Track: trackID,
					Stage: "hls.map", Line: line, Message: "illegal init uri: " + val}
			}
			track.InitURI = uri
		case "EXT-X-KEY":
			return nil, &Error{Code: ErrUnsupported, Asset: asset, Track: trackID,
				Stage: "hls.key", Line: line, Message: "encrypted media is not supported"}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, &Error{Code: ErrSyntax, Asset: asset, Stage: "hls.scan",
			Line: line, Message: err.Error()}
	}
	if havePending {
		return nil, &Error{Code: ErrSyntax, Asset: asset, Track: trackID,
			Stage: "hls.segment", Line: line, Message: "dangling EXTINF without segment"}
	}
	if len(track.Segments) == 0 {
		return nil, &Error{Code: ErrSyntax, Asset: asset, Stage: "hls.segment",
			Message: "media playlist contains no segments"}
	}
	if err := validateTimescale(track.Timescale, asset, trackID, "hls.timescale", 0); err != nil {
		return nil, err
	}
	m.DurationNS = cursorNS
	_ = mediaSeq
	_ = sawDiscontinuity
	return m, nil
}

const hlsTimescale uint64 = 1_000_000 // EXTINF floats -> microsecond resolution base

func splitTag(line string) (tag, val string) {
	line = strings.TrimPrefix(line, "#")
	if i := strings.IndexByte(line, ':'); i >= 0 {
		return line[:i], line[i+1:]
	}
	return line, ""
}

func parseEXTINF(val string) (float64, error) {
	if i := strings.IndexByte(val, ','); i >= 0 {
		val = val[:i]
	}
	d, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
	if err != nil || math.IsNaN(d) || math.IsInf(d, 0) || d <= 0 {
		return 0, &badValueError{value: val}
	}
	return d, nil
}

type badValueError struct{ value string }

func (e *badValueError) Error() string { return "illegal duration: " + e.value }

func floatDurToNS(d float64, asset, track, stage string, line int) (int64, error) {
	ns := d * 1e9
	if ns <= 0 || math.IsNaN(ns) || math.IsInf(ns, 0) || ns > float64(math.MaxInt64) {
		return 0, &Error{Code: ErrBadDuration, Asset: asset, Track: track, Stage: stage,
			Line: line, Message: "duration out of range"}
	}
	return int64(ns), nil
}

func checkNSRange(start, dur int64, asset, track, stage string, line int) error {
	if start < 0 || dur <= 0 {
		return &Error{Code: ErrBadDuration, Asset: asset, Track: track, Stage: stage,
			Line: line, Message: "negative or zero timeline position/duration"}
	}
	if start > math.MaxInt64-dur {
		return &Error{Code: ErrOverflow, Asset: asset, Track: track, Stage: stage,
			Line: line, Message: "timeline end overflows int64 nanoseconds"}
	}
	return nil
}

func hlsAttrURI(val string) (string, error) {
	for _, attr := range strings.Split(val, ",") {
		attr = strings.TrimSpace(attr)
		if strings.HasPrefix(attr, `URI="`) {
			uri := strings.TrimPrefix(attr, `URI="`)
			uri = strings.TrimSuffix(uri, `"`)
			return uri, nil
		}
	}
	return "", &badValueError{value: val}
}
