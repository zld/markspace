package container

import (
	"github.com/abema/go-mp4"

	"mediacheck/internal/media"
)

// decodeStblBox reads one sample-table box payload and records both its
// presence and its data. Presence flags drive the undecidable decision;
// absent boxes stay absent instead of being replaced by empty tables. A
// payload that fails to decode (cut index) does not abort the whole walk:
// the table is marked truncated so damage localization can bound it.
func decodeStblBox(h *mp4.ReadHandle, res *ScanResult) (interface{}, error) {
	tm := lastTrack(res)
	if tm == nil {
		return nil, nil
	}
	box, _, err := h.ReadPayload()
	if err != nil {
		idx := &tm.Index
		idx.Undecidable = true
		idx.TruncatedPayload = true
		idx.Reason = boxType(h) + " payload truncated: table undecidable"
		return nil, nil
	}
	idx := &tm.Index
	switch b := box.(type) {
	case *mp4.Stts:
		idx.SttsPresent = true
		idx.Deltas = append(idx.Deltas, b.Entries...)
	case *mp4.Stss:
		idx.StssPresent = true
		idx.SyncSamples = append(idx.SyncSamples, b.SampleNumber...)
	case *mp4.Stsc:
		idx.StscPresent = true
	case *mp4.Stsz:
		idx.StszPresent = true
		idx.SampleCount = b.SampleCount
	case *mp4.Stco:
		idx.StcoPresent = true
	case *mp4.Co64:
		idx.StcoPresent = true
	}
	return nil, nil
}

// decodeElst converts edit-list entries into nanosecond Edits. v0 entries
// carry 32-bit fields; v1 entries carry 64-bit fields. The movie time base
// is mvhd.timescale; when it is unavailable (no mvhd seen yet) the track
// timescale is used.
func decodeElst(h *mp4.ReadHandle, res *ScanResult) (interface{}, error) {
	tm := lastTrack(res)
	if tm == nil {
		return nil, nil
	}
	box, _, err := h.ReadPayload()
	if err != nil {
		return nil, err
	}
	el, ok := box.(*mp4.Elst)
	if !ok {
		return nil, nil
	}
	movieTB, err := media.NewTimeBase(effectiveScale(res, tm.Timescale))
	if err != nil {
		return nil, err
	}
	trackTB, err := media.NewTimeBase(tm.Timescale)
	if err != nil {
		return nil, err
	}
	for _, e := range el.Entries {
		var segDur, mediaTime int64
		if el.GetVersion() == 1 {
			segDur = int64(e.SegmentDurationV1)
			mediaTime = e.MediaTimeV1
		} else {
			segDur = int64(e.SegmentDurationV0)
			mediaTime = int64(e.MediaTimeV0)
		}
		ed := media.Edit{
			MovieDurationNanos: movieTB.ToNanos(segDur),
			MediaStartNanos:    trackTB.ToNanos(mediaTime),
		}
		// Empty edit: media time -1 stays negative by construction.
		tm.Edits = append(tm.Edits, ed)
	}
	return nil, nil
}

func effectiveScale(res *ScanResult, fallback uint32) uint32 {
	if res.MovieScale != 0 {
		return res.MovieScale
	}
	if fallback != 0 {
		return fallback
	}
	return 1
}

// Materialize expands stts runs into per-sample RawSample entries. Sync
// samples come from stss; when stss is absent for video, only sample 1 is
// treated as a sync point (ISOBMFF default). When the index is undecidable,
// the tail beyond the last complete stts run is filled with inferred points
// using the final run's delta, so truncation is localized rather than hidden.
func Materialize(tm *TrackModel) []media.RawSample {
	sync := map[uint32]bool{}
	for _, n := range tm.Index.SyncSamples {
		sync[n] = true
	}
	out := make([]media.RawSample, 0, tm.Index.SampleCount)
	var tick int64
	var index uint32 = 1
	for _, run := range tm.Index.Deltas {
		for k := uint32(0); k < run.SampleCount; k++ {
			isSync := sync[index]
			if !tm.Index.StssPresent && tm.Kind() == media.KindVideo && index == 1 {
				isSync = true
			}
			out = append(out, media.RawSample{
				MediaTick:    tick,
				DurationTick: run.SampleDelta,
				IsSync:       isSync,
				Source:       media.SourceDecoded,
			})
			tick += int64(run.SampleDelta)
			index++
		}
	}
	if tm.Index.Undecidable && tm.Index.SampleCount > uint32(len(out)) {
		delta := uint32(0)
		if n := len(tm.Index.Deltas); n > 0 {
			delta = tm.Index.Deltas[n-1].SampleDelta
		}
		for uint32(len(out)) < tm.Index.SampleCount && delta > 0 {
			out = append(out, media.RawSample{
				MediaTick:    tick,
				DurationTick: delta,
				IsSync:       false,
				Source:       media.SourceInferred,
			})
			tick += int64(delta)
		}
	}
	tm.RawSamples = out
	return out
}

// Scan opens, pre-scans and parses a file in one call, enforcing the input
// size limit before any parse work.
func Scan(path string) (*ScanResult, error) {
	f, err := openLimited(path)
	if err != nil {
		return nil, err
	}
	pre, err := PreScanFile(f)
	_ = f.Close()
	if err != nil {
		return nil, err
	}
	res, err := Parse(path, pre)
	if err != nil {
		return nil, err
	}
	for i := range res.Tracks {
		Materialize(&res.Tracks[i])
	}
	return res, nil
}
