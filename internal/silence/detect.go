// Package silence detects quiet spans in audio tracks on the shared timeline.
package silence

import (
	"encoding/binary"
	"io"
	"os"
	"sort"

	"github.com/pion/rtp"

	"mediaregress/internal/media"
	"mediaregress/internal/split"
	"mediaregress/internal/timeline"
	"mediaregress/internal/track"
)

// DefaultWindow is the analysis window. Windows are anchored to absolute
// timeline instants, not to segment starts, so a window is unaffected by where
// a segment boundary happens to fall.
const DefaultWindow = 100 * media.Millisecond

// Threshold is the peak amplitude (absolute int16 counts) at or below which a
// window is judged silent. It is recorded with every result so a verdict can be
// re-evaluated without re-running detection.
type Threshold int32

// WindowResult is one analysis window.
type WindowResult struct {
	Index     int          `json:"index"`
	Window    media.Window `json:"window"`
	Peak      int32        `json:"peak"`
	Threshold Threshold    `json:"threshold"`
	Silent    bool         `json:"silent"`
	Segments  []string     `json:"segments"`
}

// Range is a merged silent span.
type Range struct {
	Track        string       `json:"track"`
	Window       media.Window `json:"window"`
	Peak         int32        `json:"peak"` // max peak across the range
	Threshold    Threshold    `json:"threshold"`
	Segments     []string     `json:"segments"`
	SourceOffset int64        `json:"source_offset"`
	ParseVersion string       `json:"parse_version"`
	Stage        media.Stage  `json:"stage"`
}

// Result is the outcome for one audio track.
type Result struct {
	Track         string         `json:"track"`
	Windows       []WindowResult `json:"windows"`
	Silent        []Range        `json:"silent_ranges"`
	Threshold     Threshold      `json:"threshold"`
	ParseVersion  string         `json:"parse_version"`
	Truncated     bool           `json:"truncated,omitempty"`
	TruncatedFrom media.Ticks    `json:"truncated_from,omitempty"`
	TruncatedTo   media.Ticks    `json:"truncated_to,omitempty"`
	Errors        []*media.Error `json:"errors,omitempty"`
}

// Detect analyses every audio track. Segmentation is supplied so each window
// records which segments it touches; segment boundaries never split a window.
func Detect(path string, tl *timeline.Timeline, segs []split.Segment, window media.Ticks, threshold Threshold) ([]Result, []*media.Error, error) {
	if tl == nil {
		return nil, nil, media.NewError(media.ClassDependency, media.StageSilence, "", -1, "no timeline")
	}
	if window <= 0 {
		return nil, nil, media.Errorf(media.ClassInvalidTimebase, media.StageSilence, "", -1,
			"silence window %d must be positive", window)
	}
	hasAudio := false
	for _, tt := range tl.Tracks {
		if tt.Kind == track.KindAudio && len(tt.Samples) > 0 {
			hasAudio = true
		}
	}
	if !hasAudio {
		return nil, nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, media.WrapError(media.ClassCorruptInput, media.StageSilence, "", 0,
			"open input for audio read", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, nil, media.WrapError(media.ClassCorruptInput, media.StageSilence, "", 0, "stat input", err)
	}
	var results []Result
	var errs []*media.Error
	for _, tt := range tl.Tracks {
		if tt.Kind != track.KindAudio {
			continue
		}
		res := detectTrack(f, fi.Size(), tt, segs, window, threshold)
		results = append(results, res)
		errs = append(errs, res.Errors...)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Track < results[j].Track })
	return results, errs, nil
}

func detectTrack(f *os.File, fileSize int64, tt *timeline.TrackTimeline, segs []split.Segment, window media.Ticks, threshold Threshold) Result {
	res := Result{Track: tt.TrackID, Threshold: threshold, ParseVersion: tt.ParseVersion}
	if tt.Truncated {
		// The parser already proved the tail is missing. Record the affected
		// interval up front so the result cannot be mistaken for a complete
		// pass even if every readable sample is analysed.
		res.Truncated = true
		res.TruncatedFrom = tt.TruncatedAt
		res.TruncatedTo = tt.DeclaredDuration
		if res.TruncatedTo < res.TruncatedFrom {
			res.TruncatedTo = res.TruncatedFrom
		}
		off := tt.TruncatedOffset
		if off < 0 {
			off = -1
		}
		res.Errors = append(res.Errors, media.Errorf(media.ClassSilenceTruncated, media.StageSilence,
			tt.TrackID, off, "audio track truncated at %d ns; detection limited to readable samples", tt.TruncatedAt))
	}
	if len(tt.Samples) == 0 {
		res.Errors = append(res.Errors, media.NewError(media.ClassCorruptInput, media.StageSilence,
			tt.TrackID, -1, "audio track has no samples"))
		return res
	}
	start := tt.Samples[0].Start
	end := tt.Samples[len(tt.Samples)-1].End()
	var segsForTrack []split.Segment
	for _, s := range segs {
		if s.Track == tt.TrackID {
			segsForTrack = append(segsForTrack, s)
		}
	}

	index := 0
	var prevSeq uint16
	var haveSeq bool
	var cur WindowResult
	curStart := start
	curPeak := int32(0)
	flush := func(nextStart media.Ticks) {
		if nextStart <= curStart {
			return
		}
		cur = WindowResult{
			Index:     index,
			Window:    media.Window{Start: curStart, End: nextStart},
			Peak:      curPeak,
			Threshold: threshold,
			Silent:    curPeak <= int32(threshold),
			Segments:  overlappingSegments(segsForTrack, curStart, nextStart),
		}
		res.Windows = append(res.Windows, cur)
		index++
		curPeak = 0
		curStart = nextStart
	}

	for _, s := range tt.Samples {
		// The parser drops samples it could not read, but the file may have
		// been shortened after parsing. Either way, stop at the first sample
		// whose bytes are not present and report the affected interval.
		if tt.Truncated && s.Start >= tt.TruncatedAt {
			break
		}
		if s.Offset < 0 || s.Size == 0 || s.Offset+int64(s.Size) > fileSize {
			res.Truncated = true
			res.TruncatedFrom = s.Start
			res.TruncatedTo = end
			res.Errors = append(res.Errors, media.Errorf(media.ClassSilenceTruncated, media.StageSilence,
				tt.TrackID, fileSize, "audio sample %d is truncated; detection stopped at %d ns", s.Index, s.Start))
			break
		}
		buf := make([]byte, s.Size)
		if _, err := f.ReadAt(buf, s.Offset); err != nil && err != io.EOF {
			res.Truncated = true
			res.TruncatedFrom = s.Start
			res.TruncatedTo = end
			res.Errors = append(res.Errors, media.WrapError(media.ClassSilenceTruncated, media.StageSilence,
				tt.TrackID, s.Offset, "cannot read audio sample", err))
			break
		}
		// Audio samples in this profile are RTP-framed. A malformed or
		// non-RTP packet is analysed as raw PCM rather than rejected: an MXF
		// essence element carries plain samples, and refusing to measure it
		// would silently hide silence. Sequence gaps are diagnostics, so one
		// lost packet does not erase a verdict.
		payload := buf
		if pkt, ok := rtpPacket(buf); ok {
			if haveSeq && pkt.SequenceNumber != prevSeq+1 {
				res.Errors = append(res.Errors, media.Errorf(media.ClassCorruptInput, media.StageSilence,
					tt.TrackID, s.Offset, "RTP sequence gap: %d after %d", pkt.SequenceNumber, prevSeq))
			}
			prevSeq, haveSeq = pkt.SequenceNumber, true
			payload = pkt.Payload
		}
		peak := peakAmplitude(payload)
		if peak > curPeak {
			curPeak = peak
		}
		// Windows are anchored to absolute multiples of the window length on
		// the shared timeline, so results do not shift with a track's start.
		// A sample straddling a boundary contributes its peak to both windows:
		// carrying it forward keeps a loud edge sample from being reported as
		// silence merely because the boundary fell mid-sample.
		for {
			b := nextWindowEnd(curStart, window)
			if b > s.End() {
				break
			}
			flush(b)
			if b < s.End() {
				curPeak = peak
			}
		}
	}
	if curStart < end {
		flush(end)
	}
	res.Silent = mergeSilent(res.Windows, tt.TrackID, threshold, tt)
	return res
}

// rtpPacket decodes an RTP header only when the bytes look like RTP: version 2
// and a dynamic payload type. Raw PCM commonly starts with zero bytes, which
// would otherwise decode as a bogus version-0 packet.
func rtpPacket(buf []byte) (*rtp.Packet, bool) {
	if len(buf) < 12 || buf[0]>>6 != 2 {
		return nil, false
	}
	pt := buf[1] & 0x7f
	if pt < 96 || pt > 127 {
		return nil, false
	}
	var pkt rtp.Packet
	if err := pkt.Unmarshal(buf); err != nil {
		return nil, false
	}
	return &pkt, true
}

// nextWindowEnd returns the next absolute window boundary after start.
func nextWindowEnd(start, window media.Ticks) media.Ticks {
	return (start/window + 1) * window
}

// peakAmplitude treats the payload as little-endian signed 16-bit PCM and
// returns the maximum absolute sample.
func peakAmplitude(payload []byte) int32 {
	var peak int32
	for i := 0; i+1 < len(payload); i += 2 {
		v := int32(int16(binary.LittleEndian.Uint16(payload[i:])))
		if v < 0 {
			v = -v
		}
		if v > peak {
			peak = v
		}
	}
	return peak
}

// mergeSilent coalesces adjacent silent windows into ranges.
func mergeSilent(windows []WindowResult, trackID string, threshold Threshold, tt *timeline.TrackTimeline) []Range {
	var out []Range
	var cur *Range
	for _, w := range windows {
		if !w.Silent {
			if cur != nil {
				out = append(out, *cur)
				cur = nil
			}
			continue
		}
		if cur == nil {
			off := int64(-1)
			if s, ok := tt.SampleAt(w.Window.Start); ok {
				off = s.Offset
			}
			cur = &Range{
				Track: trackID, Window: w.Window, Peak: w.Peak,
				Threshold: threshold, Segments: append([]string(nil), w.Segments...),
				SourceOffset: off, ParseVersion: tt.ParseVersion,
				Stage: media.StageSilence,
			}
			continue
		}
		cur.Window.End = w.Window.End
		if w.Peak > cur.Peak {
			cur.Peak = w.Peak
		}
		cur.Segments = unionSegments(cur.Segments, w.Segments)
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}

func unionSegments(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string(nil), a...), b...) {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// overlappingSegments lists the segments a window touches. A window that
// straddles a boundary names both segments, which is how cross-segment windows
// remain explicit in the manifest.
func overlappingSegments(segs []split.Segment, start, end media.Ticks) []string {
	var out []string
	w := media.Window{Start: start, End: end}
	for _, s := range segs {
		if _, ok := w.Intersect(s.Window); ok {
			out = append(out, s.ID)
		}
	}
	sort.Strings(out)
	return out
}
