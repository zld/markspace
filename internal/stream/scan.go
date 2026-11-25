package stream

import (
	"fmt"
	"math"
	"sort"

	"github.com/pion/rtp"

	"github.com/zld/media-check/internal/clock"
	"github.com/zld/media-check/internal/fault"
	"github.com/zld/media-check/internal/fingerprint"
	"github.com/zld/media-check/internal/mp4"
)

// Scan validates sample boundaries and parses every RTP packet that is fully
// present. Parse issues are retained rather than replaced.
func Scan(inputID, inputSHA string, data []byte, parsed *mp4.ParseResult, maxKeyIntervalMS int64) *Result {
	if maxKeyIntervalMS <= 0 {
		maxKeyIntervalMS = DefaultMaxKeyframeIntervalMS
	}
	res := &Result{InputID: inputID, InputSHA256: inputSHA}
	if parsed != nil {
		res.Issues = append(res.Issues, parsed.Issues...)
	}
	if parsed == nil || parsed.Container == nil || parsed.Container.Manifest == nil || parsed.Container.Mdat == nil {
		if !hasIssue(res.Issues) {
			res.Issues = append(res.Issues, fault.E(fault.StageScan, fault.CodeInvalidBox,
				"cannot scan without mcmf and mdat"))
		}
		res.FirstBadOffset = firstOffset(res.Issues)
		return res
	}

	man := parsed.Container.Manifest
	mdatPayload := parsed.Container.Mdat.PayloadOffset
	byTrack := make(map[uint32][]mp4.Sample)
	for _, s := range man.Samples {
		byTrack[s.TrackID] = append(byTrack[s.TrackID], s)
	}

	for _, track := range man.Tracks {
		tr := TrackReport{Track: track, SyncIndexes: []int{}, Intervals: []KeyframeInterval{}}
		if track.Timescale == 0 {
			res.Issues = append(res.Issues, fault.E(fault.StageScan, fault.CodeInvalidTimescale,
				"track timescale must be positive", fault.WithInput(inputID, inputSHA),
				fault.WithTrack(track.TrackID)))
			res.Tracks = append(res.Tracks, tr)
			continue
		}
		trackRange, err := clock.NewRange(0, track.Duration, track.Timescale)
		if err != nil {
			res.Issues = append(res.Issues, fault.Wrap(fault.StageScan, fault.CodeInvalidTimescale, err,
				"invalid track duration", fault.WithInput(inputID, inputSHA), fault.WithTrack(track.TrackID)))
		}
		tr.TimeRange = trackRange

		samples := append([]mp4.Sample(nil), byTrack[track.TrackID]...)
		sort.Slice(samples, func(i, j int) bool { return samples[i].Index < samples[j].Index })
		var prev *mp4.Sample
		var prevAbs int64
		var prevRange clock.Range
		var lastSync *mp4.Sample
		var lastSyncAbs int64

		for _, s := range samples {
			s := s
			sr := SampleReport{TrackID: track.TrackID, TrackKind: track.Kind, Index: s.Index,
				AbsoluteOffset: -1, Size: s.Size, IsSync: s.IsSync, MaterialID: s.MaterialID}

			if s.PTS < 0 || s.Duration <= 0 {
				r, _ := clock.NewRange(max64(s.PTS, 0), max64(s.PTS+s.Duration, s.PTS), track.Timescale)
				res.Issues = append(res.Issues, fault.E(fault.StageScan, fault.CodeSampleOutOfRange,
					fmt.Sprintf("invalid sample timing pts=%d duration=%d", s.PTS, s.Duration),
					fault.WithInput(inputID, inputSHA), fault.WithTrack(track.TrackID),
					fault.WithTimeRange(r)))
			}
			endTicks, overflow := addChecked(s.PTS, s.Duration)
			if overflow || s.PTS < 0 || endTicks > track.Duration {
				r := clock.Range{Start: s.PTS, End: endTicks, Timescale: track.Timescale}
				res.Issues = append(res.Issues, fault.E(fault.StageScan, fault.CodeSampleOutOfRange,
					fmt.Sprintf("sample interval [%d,%d) exceeds track duration %d", s.PTS, endTicks, track.Duration),
					fault.WithInput(inputID, inputSHA), fault.WithTrack(track.TrackID),
					fault.WithTimeRange(r)))
			}
			if r, err := clock.NewRange(s.PTS, endTicks, track.Timescale); err == nil {
				sr.TimeRange = r
				if ms, err := clock.Convert(r.Start, r.Timescale, 1000); err == nil {
					sr.PTSMS = ms
				}
			}

			abs, end, ok := sampleBounds(mdatPayload, s)
			if !ok {
				res.Issues = append(res.Issues, fault.E(fault.StageScan, fault.CodeOffsetOutOfRange,
					fmt.Sprintf("invalid sample offset=%d size=%d", s.MdatOffset, s.Size),
					fault.WithInput(inputID, inputSHA), fault.WithTrack(track.TrackID),
					fault.WithSampleOffset(abs), fault.WithOffset(abs, int64(s.Size)),
					fault.WithTimeRange(sr.TimeRange)))
				res.Samples = append(res.Samples, sr)
				continue
			}
			sr.AbsoluteOffset = abs

			if end > int64(len(data)) {
				firstBad := max64(abs, int64(len(data)))
				missingLen := max64(end-firstBad, 0)
				res.Issues = append(res.Issues, fault.E(fault.StageScan, fault.CodeUnreadableSample,
					fmt.Sprintf("sample %d extends to %d but input size is %d", s.Index, end, len(data)),
					fault.WithInput(inputID, inputSHA), fault.WithTrack(track.TrackID),
					fault.WithOffset(firstBad, missingLen), fault.WithSampleOffset(abs),
					fault.WithTimeRange(sr.TimeRange)))
				res.Samples = append(res.Samples, sr)
				continue
			}

			var pkt rtp.Packet
			if err := pkt.Unmarshal(data[abs:end]); err != nil {
				res.Issues = append(res.Issues, fault.Wrap(fault.StageScan, fault.CodeReadFailed, err,
					"cannot parse RTP packet", fault.WithInput(inputID, inputSHA),
					fault.WithTrack(track.TrackID), fault.WithOffset(abs, int64(s.Size)),
					fault.WithTimeRange(sr.TimeRange)))
				res.Samples = append(res.Samples, sr)
				continue
			}
			sr.RTPOK = true
			sr.Readable = true
			sr.PayloadSHA256 = fingerprint.Bytes(pkt.Payload).SHA256
			tr.ReadableSamples++

			if prev != nil && s.PTS < prev.PTS {
				res.Issues = append(res.Issues, fault.E(fault.StageScan, fault.CodeTimestampRegression,
					fmt.Sprintf("PTS regressed from %d to %d", prev.PTS, s.PTS),
					fault.WithInput(inputID, inputSHA), fault.WithTrack(track.TrackID),
					fault.WithOffset(abs, int64(s.Size)), fault.WithSampleOffset(abs),
					fault.WithTimeRange(sr.TimeRange),
					fault.WithPrevious(prevAbs, prevRange)))
			}
			if prev != nil && pkt.Header.Timestamp < prev.RTPTimestamp && !rtpWrapped(prev.RTPTimestamp, pkt.Header.Timestamp) {
				res.Issues = append(res.Issues, fault.E(fault.StageScan, fault.CodeTimestampRegression,
					fmt.Sprintf("RTP timestamp regressed from %d to %d", prev.RTPTimestamp, pkt.Header.Timestamp),
					fault.WithInput(inputID, inputSHA), fault.WithTrack(track.TrackID),
					fault.WithOffset(abs, int64(s.Size)), fault.WithTimeRange(sr.TimeRange),
					fault.WithPrevious(prevAbs, prevRange)))
			}

			if track.Kind == "video" && track.KeyframeEvery > 0 && s.Index%track.KeyframeEvery == 0 && !s.IsSync {
				res.Issues = append(res.Issues, fault.E(fault.StageScan, fault.CodeMissingKeyframe,
					fmt.Sprintf("expected sync sample at index %d", s.Index),
					fault.WithInput(inputID, inputSHA), fault.WithTrack(track.TrackID),
					fault.WithOffset(abs, int64(s.Size)), fault.WithSampleOffset(abs),
					fault.WithTimeRange(sr.TimeRange)))
			}

			if s.IsSync {
				tr.SyncIndexes = append(tr.SyncIndexes, s.Index)
				if lastSync != nil {
					ticks := s.PTS - lastSync.PTS
					ms, convErr := clock.Convert(ticks, track.Timescale, 1000)
					interval := KeyframeInterval{TrackID: track.TrackID, FromIndex: lastSync.Index,
						ToIndex: s.Index, FromOffset: lastSyncAbs, ToOffset: abs, IntervalMS: ms,
						MaximumMS: maxKeyIntervalMS, Exceeded: ms > maxKeyIntervalMS}
					if convErr == nil {
						if r, err := clock.NewRange(lastSync.PTS, s.PTS, track.Timescale); err == nil {
							interval.TimeRange = r
						}
					}
					tr.Intervals = append(tr.Intervals, interval)
					if ms > tr.MaxIntervalMS {
						tr.MaxIntervalMS = ms
					}
					if interval.Exceeded {
						res.Issues = append(res.Issues, fault.E(fault.StageScan, fault.CodeKeyframeInterval,
							fmt.Sprintf("keyframe interval %dms exceeds limit %dms", ms, maxKeyIntervalMS),
							fault.WithInput(inputID, inputSHA), fault.WithTrack(track.TrackID),
							fault.WithOffset(abs, int64(s.Size)), fault.WithSampleOffset(abs),
							fault.WithTimeRange(interval.TimeRange),
							fault.WithPrevious(lastSyncAbs, interval.TimeRange)))
					}
				}
				lastSync = &s
				lastSyncAbs = abs
			}

			prev = &s
			prevAbs = abs
			prevRange = sr.TimeRange
			tr.TotalSamples++
			res.Samples = append(res.Samples, sr)
		}
		res.Tracks = append(res.Tracks, tr)
	}

	res.FirstBadOffset = firstOffset(res.Issues)
	return res
}

func sampleBounds(mdatPayload int64, s mp4.Sample) (abs, end int64, ok bool) {
	if mdatPayload < 0 || s.MdatOffset < 0 || s.Size < 0 {
		return 0, 0, false
	}
	abs, overflow := addChecked(mdatPayload, s.MdatOffset)
	if overflow {
		return abs, 0, false
	}
	end, overflow = addChecked(abs, int64(s.Size))
	if overflow {
		return abs, end, false
	}
	return abs, end, true
}

func addChecked(a, b int64) (int64, bool) {
	if b < 0 || a > math.MaxInt64-b {
		return 0, true
	}
	return a + b, false
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func rtpWrapped(prev, cur uint32) bool {
	return prev > math.MaxUint32-1000 && cur < 1000
}

func hasIssue(issues []*fault.Error) bool { return len(issues) > 0 }

func firstOffset(issues []*fault.Error) int64 {
	var first int64
	for _, e := range issues {
		if e == nil || e.Offset <= 0 {
			continue
		}
		if first == 0 || e.Offset < first {
			first = e.Offset
		}
	}
	return first
}
