package main

import (
	"fmt"
	"strings"

	"archivecheck/internal/manifest"
	"archivecheck/internal/stream"
	"archivecheck/internal/subtitle"
	"archivecheck/internal/timecode"
)

// collectStructure analyses the MP4 and RTP sources and records tracks,
// damage spans and time ranges.
func collectStructure(res *analysis, byName map[string]*sample) error {
	for _, name := range []string{"clip-valid.mp4", "clip-truncated.mp4"} {
		src := byName["samples/"+name].source
		st := stream.AnalyzeMP4(src)
		res.mp4[name] = st
		for _, span := range st.Damaged {
			res.damage = append(res.damage, manifest.Damage{
				Source: src.Name, Start: span.Start, End: span.End, Kind: span.Kind, Detail: span.Detail,
			})
		}
		for i, tr := range st.Tracks {
			res.tracks = append(res.tracks, manifest.TrackRef{
				Source: src.Name, Index: i, ID: tr.ID, Handler: tr.Handler, Timescale: tr.Timescale,
				DurationNanos: tr.DurationNanos, SampleCount: tr.SampleCount, SyncSamples: tr.SyncSamples,
				IntervalNanos: tr.Interval,
			})
			if len(tr.Keyframes) > 0 {
				res.ranges = append(res.ranges, manifest.TimeRange{
					Source: src.Name, Track: "track-" + fmt.Sprint(tr.ID),
					Start: tr.Keyframes[0].Time, End: tr.Keyframes[0].Time + tr.DurationNanos,
				})
			}
		}
	}
	rtpSrc := byName["samples/capture-loss.rtp"].source
	res.rtp = stream.AnalyzeRTP(rtpSrc)
	for _, span := range res.rtp.Damaged {
		res.damage = append(res.damage, manifest.Damage{
			Source: rtpSrc.Name, Start: span.Start, End: span.End, Kind: span.Kind, Detail: span.Detail,
		})
	}
	for _, gap := range res.rtp.Gaps {
		res.damage = append(res.damage, manifest.Damage{
			Source: rtpSrc.Name, Start: gap.Offset, End: gap.Offset + 4, Kind: "sequence-gap",
			Detail: fmt.Sprintf("expected %d, got %d, missing %d", gap.Expected, gap.Got, gap.Missing),
		})
	}
	if res.rtp.Duration > 0 {
		res.ranges = append(res.ranges, manifest.TimeRange{
			Source: rtpSrc.Name, Start: res.rtp.FirstTime, End: res.rtp.LastTime,
		})
	}
	return nil
}

// collectSubtitles parses both subtitle sources, recording cue ranges and
// every recoverable problem as a damaged byte interval.
func collectSubtitles(res *analysis, byName map[string]*sample) {
	res.srt = subtitle.Parse(byName["samples/subs-recoverable.srt"].source.Name, byName["samples/subs-recoverable.srt"].source.Bytes())
	res.vtt = subtitle.Parse(byName["samples/subs-valid.vtt"].source.Name, byName["samples/subs-valid.vtt"].source.Bytes())
	for _, tr := range []*subtitle.Track{res.srt, res.vtt} {
		if rr, ok := cueRange(tr); ok {
			res.ranges = append(res.ranges, rr)
		}
		for _, p := range tr.Problems {
			fe := faultOf(p.Err)
			res.damage = append(res.damage, manifest.Damage{
				Source: tr.Source, Start: p.Offset, End: p.End, Kind: fe.Kind, Detail: fe.Detail,
			})
		}
	}
}

// collectTimecodes parses each row of the generated timecode table with the
// multi-format parser, keeping successes and rejections apart.
func collectTimecodes(res *analysis, byName map[string]*sample) {
	codes := byName["samples/timecodes.txt"].source
	var offset int64
	for _, line := range strings.Split(string(codes.Bytes()), "\n") {
		lineOffset := offset
		offset += int64(len(line)) + 1
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		parts := strings.SplitN(trimmed, "|", 3)
		if len(parts) != 3 {
			res.bad = append(res.bad, newFault(lineOffset, "malformed timecode row"))
			continue
		}
		rate, ok := timecode.RateFor(parts[1])
		if !ok {
			res.bad = append(res.bad, newFault(lineOffset, "unknown rate "+parts[1]))
			continue
		}
		v, err := timecode.ParseAt(parts[2], rate, lineOffset+int64(strings.Index(line, parts[2])))
		if err != nil {
			res.bad = append(res.bad, err)
			continue
		}
		res.codes = append(res.codes, v)
	}
}
