package probe

import (
	"bytes"

	"tracecheck/internal/clock"
	"tracecheck/internal/fixture"
)

// RecoveredSample is a verified sample mapped onto the shared clock.
type RecoveredSample struct {
	TrackID uint32
	Index   uint32
	Offset  clock.Offset // original file byte position, never rebased
	Size    int64
	Start   clock.Tick // start time on the 90 kHz grid
	Duration clock.Tick
	Sync    bool
}

// RecoveryEvent records one recovery decision for the report.
type RecoveryEvent struct {
	Category clock.Category
	Offset   clock.Offset
	Detail   string
}

// RecoveryResult is the audited output of the recovery stage.
type RecoveryResult struct {
	Tracks map[uint32][]RecoveredSample
	Events []RecoveryEvent
	// LastSafeOffset is the highest verified sample start a checkpoint may
	// resume from.
	LastSafeOffset clock.Offset
}

// Recover validates every indexed sample against magic and CRC, rebuilds
// boundaries by frame magic when the index is missing, skips corrupt samples,
// and resumes at the next verifiable sync sample. No verifiable sync at or
// after a corrupt region yields ErrUnrecoverable.
func Recover(data []byte, tracks []*Track, mdat *Node) (*RecoveryResult, error) {
	res := &RecoveryResult{Tracks: make(map[uint32][]RecoveredSample)}
	if mdat == nil {
		return nil, &clock.ErrUnrecoverable{Reason: "no mdat box present", Offset: 0}
	}
	for _, tr := range tracks {
		samples, err := recoverTrack(data, tr, mdat, res)
		if err != nil {
			return nil, err
		}
		res.Tracks[tr.ID] = samples
		if n := len(samples); n > 0 && samples[n-1].Offset > res.LastSafeOffset {
			res.LastSafeOffset = samples[n-1].Offset
		}
	}
	return res, nil
}

func recoverTrack(data []byte, tr *Track, mdat *Node, res *RecoveryResult) ([]RecoveredSample, error) {
	if len(tr.MissingIndex) == 0 {
		return verifyIndexed(data, tr, res)
	}
	res.Events = append(res.Events, RecoveryEvent{
		Category: clock.CategoryMissingIndex,
		Offset:   mdat.Offset,
		Detail:   "track " + itoa(tr.ID) + " index incomplete; scanning frame magic",
	})
	return rebuildByMagic(data, tr, mdat, res)
}

// mapTime converts one indexed sample's start (index*delta source units) and
// duration through the single track converter, recording any flooring loss.
func mapTime(tr *Track, sampleNo int, delta uint32, off clock.Offset, res *RecoveryResult) (clock.Tick, clock.Tick) {
	startConv, err := tr.Converter.ConvertFloor(uint64(sampleNo) * uint64(delta))
	if err != nil {
		return 0, 0
	}
	if startConv.Truncated {
		res.Events = append(res.Events, ConversionEvent(off, startConv.Remainder))
	}
	durConv, err := tr.Converter.ConvertFloor(uint64(delta))
	if err != nil {
		return startConv.Ticks, 0
	}
	return startConv.Ticks, durConv.Ticks
}

// verifyIndexed walks the surviving sample table, bounds-checking each range.
func verifyIndexed(data []byte, tr *Track, res *RecoveryResult) ([]RecoveredSample, error) {
	var out []RecoveredSample
	skipping := false
	fileLen := int64(len(data))
	for i, rs := range tr.Samples {
		if _, _, err := clock.Checked(rs.Offset, rs.Size, fileLen, "sample"); err != nil {
			return nil, clock.NewOffsetError(rs.Offset, clock.CategoryBounds,
				"sample range outside file", err)
		}
		raw := data[int64(rs.Offset) : int64(rs.Offset)+rs.Size]
		start, dur := mapTime(tr, i, rs.Delta, rs.Offset, res)
		if _, ok := fixture.VerifyFrame(raw); !ok {
			res.Events = append(res.Events, RecoveryEvent{
				Category: clock.CategoryCorruptSample,
				Offset:   rs.Offset,
				Detail:   "sample " + itoa(uint32(i)) + " failed magic/CRC",
			})
			skipping = true
			continue
		}
		if skipping {
			if !rs.Sync {
				continue // keep dropping until a verifiable sync sample
			}
			res.Events = append(res.Events, RecoveryEvent{
				Category: EventResumed,
				Offset:   rs.Offset,
				Detail:   "resumed at verifiable sync sample " + itoa(uint32(i)),
			})
			skipping = false
		}
		out = append(out, RecoveredSample{
			TrackID: tr.ID, Index: uint32(i), Offset: rs.Offset, Size: rs.Size,
			Start: start, Duration: dur, Sync: rs.Sync,
		})
	}
	if skipping {
		return nil, &clock.ErrUnrecoverable{
			Reason: "corrupt region has no later verifiable sync sample",
			Offset: tr.Samples[len(tr.Samples)-1].Offset,
		}
	}
	return out, nil
}

// rebuildByMagic locates frames without a sample table and keeps this track's
// frames, applying the same corrupt-skip / sync-resume rules.
func rebuildByMagic(data []byte, tr *Track, mdat *Node, res *RecoveryResult) ([]RecoveredSample, error) {
	region := data[int64(mdat.PayloadOff) : int64(mdat.PayloadOff)+mdat.PayloadLen]
	magic := []byte(fixture.SampleMagic)
	var out []RecoveredSample
	skipping := false
	pos := 0
	for pos < len(region) {
		idx := bytes.Index(region[pos:], magic)
		if idx < 0 {
			break
		}
		abs := int64(mdat.PayloadOff) + int64(pos) + int64(idx)
		frame, size, ok := readFrameAt(data, abs, int64(len(data)))
		if !ok || frame.TrackID != tr.ID {
			pos = pos + idx + 1
			continue
		}
		sampleNo := int(frame.Index)
		delta := deltaFor(tr, sampleNo)
		start, dur := mapTime(tr, sampleNo, delta, clock.Offset(abs), res)
		sync := tr.isSync(sampleNo)
		pos = int(abs-int64(mdat.PayloadOff)) + int(size)
		if !ok {
			res.Events = append(res.Events, RecoveryEvent{
				Category: clock.CategoryCorruptSample,
				Offset:   clock.Offset(abs),
				Detail:   "magic frame failed verification",
			})
			skipping = true
			continue
		}
		if skipping && !sync {
			continue
		}
		if skipping {
			res.Events = append(res.Events, RecoveryEvent{
				Category: EventResumed,
				Offset:   clock.Offset(abs),
				Detail:   "magic scan resumed at sync sample " + itoa(uint32(sampleNo)),
			})
			skipping = false
		}
		out = append(out, RecoveredSample{
			TrackID: tr.ID, Index: frame.Index, Offset: clock.Offset(abs),
			Size: int64(size), Start: start, Duration: dur, Sync: sync,
		})
	}
	if skipping {
		return nil, &clock.ErrUnrecoverable{
			Reason: "magic scan ended inside a corrupt region", Offset: mdat.Offset,
		}
	}
	if len(out) == 0 {
		return nil, &clock.ErrUnrecoverable{
			Reason: "no verifiable frame for track " + itoa(tr.ID), Offset: mdat.Offset,
		}
	}
	return out, nil
}

// readFrameAt bounds-checks and verifies one self-describing frame.
func readFrameAt(data []byte, abs, fileLen int64) (fixture.SampleFrame, int64, bool) {
	if abs+fixture.SampleHeaderSize > fileLen {
		return fixture.SampleFrame{}, 0, false
	}
	payloadLen := int64(binaryU32Local(data[abs+16 : abs+20]))
	size := fixture.SampleHeaderSize + payloadLen
	if _, _, err := clock.Checked(clock.Offset(abs), size, fileLen, "frame"); err != nil {
		return fixture.SampleFrame{}, 0, false
	}
	frame, ok := fixture.VerifyFrame(data[abs : abs+size])
	return frame, size, ok
}
