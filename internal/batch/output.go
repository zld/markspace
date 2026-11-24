package batch

import (
	"strings"

	"mediapack/internal/clock"
	"mediapack/internal/demux"
	"mediapack/internal/index"
	"mediapack/internal/maptrack"
	"mediapack/internal/remux"
	"mediapack/internal/timeline"
)

type outputBuild struct {
	tracks        []remux.Track
	manifests     []index.TrackManifest
	totalSamples  int
	totalBytes    int64
	maxDuration   float64
	aggregateHash string
}

func buildOutputs(table *maptrack.Table, tl *timeline.Timeline) outputBuild {
	mapped := map[uint32]*maptrack.MappedTrack{}
	for _, m := range table.Tracks {
		mapped[m.TargetID] = m
	}
	var out outputBuild
	var trackHashes []string
	rate := uint32(tl.Timebase.Den)

	for _, plan := range tl.Tracks {
		if len(plan.Samples) == 0 {
			continue
		}
		mt := mapped[plan.TargetID]
		rt := remux.Track{ID: plan.TargetID, Timescale: rate}
		tm := index.TrackManifest{TargetID: plan.TargetID, Timescale: rate}
		if mt != nil {
			tm.Source = mt.SourcePos
			if mt.Track != nil {
				tm.Kind = mt.Track.Kind
				tm.Codec = mt.Track.Codec.Codec
				rt.Kind = mt.Track.Kind
				rt.FourCC = mt.Track.Codec.FourCC
				rt.Width = mt.Track.Codec.Width
				rt.Height = mt.Track.Codec.Height
				rt.Channels = mt.Track.Codec.Channels
				rt.SampleRate = mt.Track.Codec.SampleRate
			} else if mt.RTPStream != nil {
				tm.Kind = "audio"
				tm.Codec = "rtp-payload"
				rt.Kind = "audio"
				rt.FourCC = "mp4a"
				rt.Channels = 1
				rt.SampleRate = mt.RTPStream.ClockRate
			}
		}
		if rt.Kind == "" {
			rt.Kind = "video"
		}

		hashes := make([]string, 0, len(plan.Samples))
		var bytes int64
		var boundaries []any
		for _, cl := range plan.Clips {
			boundaries = append(boundaries, cl)
		}
		tm.ClipBoundaries = boundaries
		for _, s := range plan.Samples {
			rt.Samples = append(rt.Samples, remux.Sample{
				DTS: s.DTS, PTS: s.PTS, Duration: s.Duration, Payload: s.Payload, Hash: s.Hash,
			})
			hashes = append(hashes, s.Hash)
			bytes += int64(len(s.Payload))
		}
		tm.SampleCount = len(hashes)
		tm.SampleHashes = hashes
		tm.PayloadBytes = bytes
		tm.PayloadHash = demux.HashPayload([]byte(strings.Join(hashes, "\n")))
		trackHashes = append(trackHashes, tm.PayloadHash)
		if n := len(plan.Samples); n > 0 {
			last := plan.Samples[n-1]
			tm.DurationSeconds = clock.Ticks(last.DTS+last.Duration, tl.Timebase).Float64()
		}
		if tm.DurationSeconds > out.maxDuration {
			out.maxDuration = tm.DurationSeconds
		}
		tm.ActualBitrateBps = float64(bytes) * 8 / maxFloat(tm.DurationSeconds, 0.000001)
		out.totalSamples += len(hashes)
		out.totalBytes += bytes
		out.tracks = append(out.tracks, rt)
		out.manifests = append(out.manifests, tm)
	}
	out.aggregateHash = demux.HashPayload([]byte(strings.Join(trackHashes, "\n")))
	return out
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
