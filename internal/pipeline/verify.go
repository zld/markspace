package pipeline

import (
	"time"

	"mediakernel/internal/clock"
	"mediakernel/internal/index"
	"mediakernel/internal/wav"
)

func timeNowUnixNano() int64 { return time.Now().UnixNano() }

// verifyRanges walks the whole stream as a sequence of gap-free half-open
// 10 ms intervals and checks every returned block covers whole frames.
// Truncation at the file end surfaces with the WAV reader's byte offset.
// It returns the start frame of every interval, for the query-stage digest.
func verifyRanges(r *wav.Reader, tl *clock.Timeline, idx *index.Index) ([]int64, int, error) {
	const intervalNanos int64 = 10_000_000
	h := r.Header()
	frameSize := h.FrameSize()
	totalNanos, err := tl.SampleToNanos(idx.TotalFrames)
	if err != nil {
		return nil, 0, err
	}
	var starts []int64
	count := 0
	var startNanos int64
	for startNanos < totalNanos {
		endNanos := startNanos + intervalNanos
		if endNanos > totalNanos {
			endNanos = totalNanos
		}
		res, err := idx.ReadRange(r, tl, startNanos, endNanos)
		if err != nil {
			return nil, 0, err
		}
		if int64(len(res.Bytes)) != (res.Range.End-res.Range.Start)*frameSize {
			return nil, 0, clock.NewError("pipeline", clock.CatMisaligned,
				"interval [%d,%d) returned %d bytes, not frame aligned",
				startNanos, endNanos, len(res.Bytes))
		}
		starts = append(starts, res.Range.Start)
		count++
		startNanos = endNanos
	}
	return starts, count, nil
}
