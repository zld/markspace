package ingest

import (
	"sort"

	"regress-kernel/internal/media"
)

// Align projects both tracks onto the shared nanosecond time base and pairs
// every video frame with its temporally nearest audio packet. The pairing is
// a pure function of the preset timestamps, so repeated runs yield identical
// SyncReports; loss-repaired audio packets participate like any other.
func Align(frames []media.Frame, audio []media.AudioPacket, clock media.Clock) media.SyncReport {
	rep := media.SyncReport{Samples: make([]media.SyncSample, 0, len(frames))}
	if len(frames) == 0 || len(audio) == 0 {
		return rep
	}
	audioNanos := make([]int64, len(audio))
	for i, ap := range audio {
		audioNanos[i] = clock.AudioToNanos(ap.RTPTimestamp)
	}
	var errSum int64
	for _, f := range frames {
		videoN := clock.VideoToNanos(f.TimestampTicks)
		idx := sort.Search(len(audioNanos), func(i int) bool {
			return audioNanos[i] >= videoN
		})
		nearest := idx
		if idx == len(audioNanos) {
			nearest = len(audioNanos) - 1
		} else if idx > 0 {
			if abs64(videoN-audioNanos[idx-1]) < abs64(audioNanos[idx]-videoN) {
				nearest = idx - 1
			}
		}
		audioN := audioNanos[nearest]
		errN := audioN - videoN // positive: audio leads video
		rep.Samples = append(rep.Samples, media.SyncSample{
			FrameIndex: f.Index, VideoNanos: videoN,
			AudioNanos: audioN, ErrorNanos: errN,
		})
		if a := abs64(errN); a > rep.MaxAbsErrorNs {
			rep.MaxAbsErrorNs = a
		}
		errSum += errN
	}
	rep.SampleCount = len(rep.Samples)
	if rep.SampleCount > 0 {
		rep.MeanErrorNs = errSum / int64(rep.SampleCount)
	}
	return rep
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// ContinuityError returns the largest presentation-time gap between adjacent
// video frames, expressed on the shared clock. Zero means a continuous track.
func ContinuityError(frames []media.Frame, clock media.Clock) int64 {
	var maxGap int64
	for i := 1; i < len(frames); i++ {
		gap := clock.VideoToNanos(frames[i].TimestampTicks) -
			clock.VideoToNanos(frames[i-1].TimestampTicks)
		if gap > maxGap {
			maxGap = gap
		}
	}
	return maxGap
}
