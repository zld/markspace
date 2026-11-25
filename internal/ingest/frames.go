// Package ingest validates generated video frames and RTP audio and aligns
// both tracks on the package-wide nanosecond time base.
package ingest

import (
	"fmt"

	"regress-kernel/internal/media"
)

// FrameLimits caps the resources a single task may consume.
type FrameLimits struct {
	MaxFrames     int
	MaxInputBytes int64
}

// DefaultFrameLimits is the permissive-but-bounded default for the fixture.
func DefaultFrameLimits() FrameLimits {
	return FrameLimits{MaxFrames: 4096, MaxInputBytes: 256 << 20}
}

// ValidateFrames checks sequence numbers, timestamps, metadata and SHA-256
// hashes. Duplicate, missing and out-of-range sequence numbers are counted
// separately; every failure records the offending frame index and makes the
// returned report fail. The error is non-nil only for hard limit violations.
func ValidateFrames(frames []media.Frame, wantWidth, wantHeight int, lim FrameLimits) (media.FrameReport, error) {
	rep := media.FrameReport{Faults: []media.FrameFault{}}
	if len(frames) > lim.MaxFrames {
		return rep, fmt.Errorf("%w: %d frames > %d",
			media.ErrLimitExceeded, len(frames), lim.MaxFrames)
	}
	var totalBytes int64
	seen := map[int]bool{}
	prevSeq := -1
	upperBound := len(frames) + 1 // one missing frame at most is pre-removed
	for i := range frames {
		f := frames[i]
		totalBytes += int64(len(f.Payload))
		switch {
		case f.Seq < 0 || f.Seq >= upperBound:
			rep.Counters.OutOfRange++
			rep.Faults = append(rep.Faults, media.FrameFault{
				Index: i, Kind: "out_of_range",
				Detail: fmt.Sprintf("seq=%d outside [0,%d)", f.Seq, upperBound),
			})
		case seen[f.Seq]:
			rep.Counters.Duplicate++
			rep.Faults = append(rep.Faults, media.FrameFault{
				Index: i, Kind: "duplicate",
				Detail: fmt.Sprintf("seq=%d seen again", f.Seq),
			})
		case prevSeq >= 0 && f.Seq > prevSeq+1:
			gap := f.Seq - prevSeq - 1
			rep.Counters.Missing += gap
			rep.Faults = append(rep.Faults, media.FrameFault{
				Index: i, Kind: "missing",
				Detail: fmt.Sprintf("%d seq gap before seq=%d", gap, f.Seq),
			})
		}
		seen[f.Seq] = true
		if f.Seq >= 0 {
			prevSeq = f.Seq
		}
		if f.Width != wantWidth || f.Height != wantHeight {
			rep.Faults = append(rep.Faults, media.FrameFault{
				Index: i, Kind: "metadata",
				Detail: fmt.Sprintf("got %dx%d want %dx%d",
					f.Width, f.Height, wantWidth, wantHeight),
			})
		}
		if i > 0 && f.TimestampTicks <= frames[i-1].TimestampTicks {
			rep.Faults = append(rep.Faults, media.FrameFault{
				Index: i, Kind: "timestamp",
				Detail: "timestamp not strictly increasing",
			})
		}
		if !f.VerifyHash() {
			rep.Counters.BadHash++
			rep.Faults = append(rep.Faults, media.FrameFault{
				Index: i, Kind: "bad_hash",
				Detail: fmt.Sprintf("declared=%s actual=%s",
					f.DeclaredSHA256[:12], media.ComputeSHA256(f.Payload)[:12]),
			})
		}
	}
	if totalBytes > lim.MaxInputBytes {
		return rep, fmt.Errorf("%w: %d bytes > %d",
			media.ErrLimitExceeded, totalBytes, lim.MaxInputBytes)
	}
	if !rep.Failed() {
		rep.Accepted = len(frames)
	}
	return rep, nil
}
