package manifest

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"

	"mediascan/internal/fault"
)

// ParseHLS parses an extended M3U media playlist. Only the tags needed for
// segment ordering, duration, sequence and discontinuity handling are
// interpreted; unknown tags are ignored per RFC 8216.
func ParseHLS(data []byte) (*Manifest, *fault.Log) {
	p := newParser()
	m := &Manifest{Format: FormatHLS, TimeScale: 1}

	var (
		pendingDur    float64
		haveDur       bool
		pendingSeq    uint64
		havePending   bool
		discontinuity bool
		nextSeq       uint64
		haveSeq       bool
		cursor        int // running byte offset of the current line
	)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lineIndex := 0
	for scanner.Scan() {
		line := scanner.Text()
		lineStart := int64(cursor)
		cursor += len(line) + 1 // account for the stripped newline
		lineIndex++
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			continue
		case strings.HasPrefix(trimmed, "#EXT-X-TARGETDURATION:"):
			if v, err := strconv.ParseFloat(tagValue(trimmed), 64); err == nil {
				m.TargetDuration = v
			}
		case strings.HasPrefix(trimmed, "#EXT-X-MEDIA-SEQUENCE:"):
			if v, err := strconv.ParseUint(tagValue(trimmed), 10, 64); err == nil {
				nextSeq, haveSeq = v, true
			}
		case trimmed == "#EXT-X-DISCONTINUITY":
			discontinuity = true
		case strings.HasPrefix(trimmed, "#EXTINF:"):
			raw := tagValue(trimmed)
			if comma := strings.IndexByte(raw, ','); comma >= 0 {
				raw = raw[:comma]
			}
			if v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64); err == nil {
				pendingDur, haveDur = v, true
			}
		case strings.HasPrefix(trimmed, "#X-MEDIASCAN-SEQ:"):
			// Explicit sequence marker emitted by the fixture generator
			// (and tolerated on externally authored playlists) so that a
			// missing or repeated chunk remains detectable even though
			// plain EXTINF lines carry no sequence number.
			if v, err := strconv.ParseUint(tagValue(trimmed), 10, 64); err == nil {
				pendingSeq, havePending = v, true
			}
		case strings.HasPrefix(trimmed, "#"):
			// Other tags (KEY, MAP, BYTERANGE, ...) do not create segments.
			continue
		default:
			if !haveDur {
				p.log.Add(fault.Record{
					Stage: fault.StageManifest, Severity: fault.SeverityError,
					Kind: fault.KindRoundtripMismatch, Segment: trimmed,
					SourceOffset: lineStart,
					Message:      "segment URI without preceding EXTINF",
				})
			}
			seq := nextSeq
			switch {
			case havePending:
				seq = pendingSeq
			case !haveSeq:
				seq = uint64(len(m.Segments))
			}
			start := 0.0
			if n := len(m.Segments); n > 0 {
				start = m.Segments[n-1].End
			}
			m.Segments = append(m.Segments, Segment{
				Index: len(m.Segments), MediaSeq: seq, URI: trimmed,
				Duration: pendingDur, Start: start, End: start + pendingDur,
				SourceOffset: lineStart, Discontinuity: discontinuity,
			})
			if discontinuity {
				p.log.Add(fault.Record{
					Stage: fault.StageManifest, Severity: fault.SeverityWarning,
					Kind: fault.KindDiscontinuity, Segment: trimmed,
					MediaSeq: seq, SourceOffset: lineStart,
					Message: "explicit discontinuity boundary",
				})
				discontinuity = false
			}
			pendingDur, haveDur = 0, false
			pendingSeq, havePending = 0, false
			nextSeq = seq + 1
		}
	}
	m.StartSeq = 0
	if len(m.Segments) > 0 {
		m.StartSeq = m.Segments[0].MediaSeq
	}
	p.checkSequences(m.Segments)
	return m, p.log
}

func tagValue(line string) string {
	if i := strings.IndexByte(line, ':'); i >= 0 {
		return strings.TrimSpace(line[i+1:])
	}
	return ""
}
