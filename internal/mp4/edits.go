package mp4

import (
	"bytes"
	"math"

	gomp4 "github.com/abema/go-mp4"

	"mediacore/internal/clock"
)

func parseEditList(r readAtSeeker, path string, b *Box, track int) ([]clock.EditEntry, error) {
	dec, err := unmarshalBox(r, path, b, track)
	if err != nil {
		return nil, err
	}
	elst, ok := dec.(*gomp4.Elst)
	if !ok {
		return nil, clock.New(clock.CategoryInvalidEditList, path, track, b.Offset, "elst has unexpected Go type")
	}
	if int(elst.EntryCount) != len(elst.Entries) {
		return nil, clock.New(clock.CategoryInvalidEditList, path, track, b.PayloadOffset+4,
			"elst entry count does not match decoded entries")
	}
	version := elst.GetVersion()
	entrySize := int64(12)
	if version == 1 {
		entrySize = 20
	}
	entriesStart := b.PayloadOffset + 8 // FullBox header + entry_count
	out := make([]clock.EditEntry, 0, len(elst.Entries))
	for i, e := range elst.Entries {
		entryOff := entriesStart + int64(i)*entrySize
		durationOff := entryOff
		var duration int64
		var mediaTime int64
		if version == 1 {
			duration = int64(e.SegmentDurationV1)
			mediaTime = e.MediaTimeV1
			if e.SegmentDurationV1 > math.MaxInt64 {
				return nil, clock.New(clock.CategoryNegativeDuration, path, track, durationOff,
					"v1 segment duration has negative signed interpretation")
			}
		} else {
			if e.SegmentDurationV0 > math.MaxInt32 {
				return nil, clock.New(clock.CategoryNegativeDuration, path, track, durationOff,
					"v0 segment duration has negative signed interpretation")
			}
			duration = int64(e.SegmentDurationV0)
			mediaTime = int64(e.MediaTimeV0)
		}
		if duration < 0 {
			return nil, clock.New(clock.CategoryNegativeDuration, path, track, durationOff,
				"negative edit segment duration")
		}
		if e.MediaRateInteger != 1 || e.MediaRateFraction != 0 {
			rateOff := entryOff + entrySize - 4
			return nil, clock.New(clock.CategoryInvalidEditList, path, track, rateOff,
				"unsupported non-unit edit rate")
		}
		out = append(out, clock.EditEntry{Duration: duration, MediaTime: mediaTime, Offset: durationOff})
	}
	return out, nil
}

func unmarshalBox(r readAtSeeker, path string, b *Box, track int) (gomp4.IBox, error) {
	payload, err := readBoxPayload(r, path, b, track)
	if err != nil {
		return nil, err
	}
	box, _, err := gomp4.UnmarshalAny(
		bytes.NewReader(payload),
		gomp4.StrToBoxType(b.Type),
		uint64(len(payload)),
		gomp4.Context{},
	)
	if err != nil {
		return nil, clock.Wrap(clock.CategoryInvalidBox, path, track, b.PayloadOffset,
			"go-mp4 could not decode "+b.Type, err)
	}
	return box, nil
}
