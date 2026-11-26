package scan

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strings"

	"mediad/internal/model"
)

// maxMetaLine bounds a single metadata line so a malformed file cannot force
// unbounded buffering. WebVTT/ID3 sidecars are small text/binary metadata.
const maxMetaLine = 64 * 1024

// ScanWebVTT parses a WebVTT sidecar into cue events. All timestamps use the
// 1/1000 WebVTT time base; malformed cue lines become UNPARSEABLE anomalies
// carrying their byte offset instead of aborting the whole scan.
func ScanWebVTT(r io.Reader, sourcePath, sourceHash string, trackID uint32) (*ScanResult, error) {
	res := &ScanResult{SourcePath: sourcePath, SourceHash: sourceHash}
	base := model.Rational{Num: 1, Den: 1000}
	res.Tracks = []TrackSummary{{
		ID: trackID, Kind: KindSubtitle, Handler: "text/vtt",
		TimeBase: base, Fragmented: false,
	}}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), maxMetaLine)
	var offset int64
	seenHeader := false
	for sc.Scan() {
		line := sc.Text()
		lineStart := offset
		offset += int64(len(sc.Bytes())) + 1 // account for the trailing newline
		trimmed := strings.TrimSpace(line)
		if !seenHeader {
			if strings.HasPrefix(trimmed, "WEBVTT") {
				seenHeader = true
			}
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "NOTE") || strings.HasPrefix(trimmed, "REGION") || strings.HasPrefix(trimmed, "STYLE") {
			continue
		}
		// A timing line contains the arrow. Cue identifier lines do not.
		arrow := strings.Index(trimmed, "-->")
		if arrow < 0 {
			continue
		}
		start, end, ok := parseCueTiming(trimmed[:arrow], trimmed[arrow+3:])
		if !ok {
			res.Anomalies = append(res.Anomalies, model.Anomaly{
				Type: model.AnomalyUnparseable, TrackID: trackID,
				Message:      "unparseable WebVTT timing: " + trimmed,
				SourceOffset: lineStart, TimeBase: base,
			})
			continue
		}
		res.Events = append(res.Events, TimedEvent{
			TrackID: trackID, Kind: EventCue, Tick: start, DurTick: end - start,
			Offset: lineStart, Length: offset - lineStart,
			TimeBase: base, ID: fmt.Sprintf("cue-%d", len(res.Events)),
		})
		res.Tracks[0].SampleCount++
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan: webvtt read: %w", err)
	}
	if !seenHeader {
		return nil, fmt.Errorf("scan: %s is not a WebVTT file", sourcePath)
	}
	return res, nil
}

// parseCueTiming parses "HH:MM:SS.mmm" style WebVTT timestamps, tolerating
// optional whitespace and a settings block after the end time.
func parseCueTiming(startRaw, endRaw string) (int64, int64, bool) {
	start, ok1 := parseVTTClock(strings.TrimSpace(startRaw))
	endFields := strings.Fields(strings.TrimSpace(endRaw))
	if len(endFields) == 0 {
		return 0, 0, false
	}
	end, ok2 := parseVTTClock(endFields[0])
	return start, end, ok1 && ok2
}

func parseVTTClock(s string) (int64, bool) {
	parts := strings.Split(s, ":")
	var hh, mm, rest string
	switch len(parts) {
	case 3:
		hh, mm, rest = parts[0], parts[1], parts[2]
	case 2:
		hh, mm, rest = "0", parts[0], parts[1]
	case 1:
		hh, mm, rest = "0", "0", parts[0]
	default:
		return 0, false
	}
	secFrac := strings.Split(rest, ".")
	if len(secFrac) != 2 {
		return 0, false
	}
	h, e1 := atoiNonNeg(hh)
	m, e2 := atoiNonNeg(mm)
	sec, e3 := atoiNonNeg(secFrac[0])
	frac := secFrac[1]
	if len(frac) > 3 {
		frac = frac[:3]
	}
	ms, e4 := atoiNonNeg(frac)
	for len(frac) < 3 {
		ms *= 10
		frac += "0"
	}
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
		return 0, false
	}
	return int64(h)*3_600_000 + int64(m)*60_000 + int64(sec)*1000 + int64(ms), true
}

// ScanChapters parses a simple chapter sidecar (CHAPTERnn=clock /
// CHAPTERnnNAME=title pairs, one per line).
func ScanChapters(r io.Reader, sourcePath, sourceHash string, trackID uint32) (*ScanResult, error) {
	res := &ScanResult{SourcePath: sourcePath, SourceHash: sourceHash}
	base := model.Rational{Num: 1, Den: 1000}
	res.Tracks = []TrackSummary{{
		ID: trackID, Kind: KindChapter, Handler: "chapters",
		TimeBase: base,
	}}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), maxMetaLine)
	var offset int64
	ticks := map[string]int64{}
	for sc.Scan() {
		line := sc.Text()
		lineStart := offset
		offset += int64(len(sc.Bytes())) + 1
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		key, val, found := strings.Cut(trimmed, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		if strings.HasPrefix(key, "CHAPTER") && !strings.HasSuffix(key, "NAME") {
			if t, ok := parseVTTClock(strings.TrimSpace(val)); ok {
				ticks[key] = t
			}
		}
		if strings.HasSuffix(key, "NAME") {
			baseKey := strings.TrimSuffix(key, "NAME")
			t, ok := ticks[baseKey]
			if !ok {
				continue
			}
			res.Events = append(res.Events, TimedEvent{
				TrackID: trackID, Kind: EventChapter, Tick: t,
				Offset: lineStart, Length: offset - lineStart,
				TimeBase: base, ID: baseKey,
				Payload: []byte(strings.TrimSpace(val)),
			})
			res.Tracks[0].SampleCount++
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan: chapters read: %w", err)
	}
	return res, nil
}

func atoiNonNeg(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("empty number")
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("bad digit %q", c)
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// ScanID3 parses a sidecar holding concatenated ID3v2.3/2.4 tags. Each tag's
// TXXX/PRIV frame may carry a big-endian uint32 presentation time in
// milliseconds; frames without one still appear as events at tick 0 with
// their offset preserved.
func ScanID3(r io.Reader, sourcePath, sourceHash string, trackID uint32) (*ScanResult, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxID3Bytes))
	if err != nil {
		return nil, fmt.Errorf("scan: id3 read: %w", err)
	}
	res := &ScanResult{SourcePath: sourcePath, SourceHash: sourceHash}
	base := model.Rational{Num: 1, Den: 1000}
	res.Tracks = []TrackSummary{{
		ID: trackID, Kind: KindID3, Handler: "id3", TimeBase: base,
	}}
	p := newByteReader(data)
	for len(p.b[p.i:]) >= 10 {
		tagOff := p.i
		if !bytes.Equal(p.b[p.i:p.i+3], []byte("ID3")) {
			return nil, fmt.Errorf("scan: bad ID3 magic at offset %d", tagOff)
		}
		major := p.b[p.i+3]
		_ = major
		size := syncsafeSize(p.b[p.i+6 : p.i+10])
		p.i += 10
		tagEnd := p.i + size
		if tagEnd > len(p.b) {
			res.Anomalies = append(res.Anomalies, model.Anomaly{
				Type: model.AnomalyUnparseable, TrackID: trackID,
				Message: "truncated ID3 tag", SourceOffset: int64(tagOff),
			})
			break
		}
		for p.i+10 <= tagEnd {
			frameOff := p.i
			fid := string(p.b[p.i : p.i+4])
			if fid == "\x00\x00\x00\x00" {
				break
			}
			fsize := int(binary.BigEndian.Uint32(p.b[p.i+4 : p.i+8]))
			p.i += 10
			if p.i+fsize > tagEnd {
				res.Anomalies = append(res.Anomalies, model.Anomaly{
					Type: model.AnomalyUnparseable, TrackID: trackID,
					Message: "truncated ID3 frame " + fid, SourceOffset: int64(frameOff),
				})
				p.i = tagEnd
				break
			}
			body := p.b[p.i : p.i+fsize]
			p.i += fsize
			tick := extractTick(body)
			res.Events = append(res.Events, TimedEvent{
				TrackID: trackID, Kind: EventID3, Tick: tick,
				Offset: int64(frameOff), Length: int64(fsize + 10),
				TimeBase: base, ID: fid, Payload: append([]byte(nil), body...),
			})
			res.Tracks[0].SampleCount++
		}
		p.i = tagEnd
	}
	return res, nil
}

// maxID3Bytes bounds a sidecar of concatenated tags.
const maxID3Bytes = 4 << 20 // 4 MiB

// syncsafeSize decodes a 28-bit syncsafe integer (7 useful bits per byte).
func syncsafeSize(b []byte) int {
	return int(b[0])<<21 | int(b[1])<<14 | int(b[2])<<7 | int(b[3])
}

// extractTick reads a leading big-endian uint32 millisecond timestamp that our
// generated sidecars prepend to frame payloads. Absent data yields tick 0.
func extractTick(body []byte) int64 {
	if len(body) >= 4 {
		return int64(binary.BigEndian.Uint32(body[:4]))
	}
	return 0
}
