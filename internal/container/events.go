package container

import (
	"fmt"

	"github.com/zld/mediarc/internal/model"
)

func (tr *Track) buildEvents(trackID int, label string, sttsOffset int64, cfg model.Config) ([]model.RawEvent, error) {
	if tr.Stts == nil || len(tr.Stts.Entries) == 0 {
		return []model.RawEvent{}, nil
	}
	bits := cfg.WrapBits
	if bits == 0 {
		bits = 32
	}
	var mask uint64
	if bits >= 64 {
		mask = ^uint64(0)
	} else {
		mask = (uint64(1) << bits) - 1
	}

	events := make([]model.RawEvent, 0, min(cfg.Limits.MaxBufferedEvents, 128))
	var cursor uint64
	sampleNumber := uint32(1)
	for _, entry := range tr.Stts.Entries {
		for sample := uint32(0); sample < entry.SampleCount; sample++ {
			if len(events) >= cfg.Limits.MaxBufferedEvents {
				return nil, &model.ParseError{
					TrackID:    trackID,
					BoxType:    "stts",
					ByteOffset: sttsOffset,
					Err:        fmt.Errorf("sample count exceeds event limit %d", cfg.Limits.MaxBufferedEvents),
				}
			}
			raw := cursor & mask
			rawNS, err := model.ToNS(raw, tr.Mdhd.Timescale)
			if err != nil {
				return nil, &model.ParseError{TrackID: trackID, BoxType: "mdhd/stts", ByteOffset: sttsOffset, Err: err}
			}
			durationNS, err := model.ToNS(uint64(entry.SampleDelta), tr.Mdhd.Timescale)
			if err != nil {
				return nil, &model.ParseError{TrackID: trackID, BoxType: "stts", ByteOffset: sttsOffset, Err: err}
			}
			events = append(events, model.RawEvent{
				TrackID:        uint32(trackID),
				Representation: label,
				SampleNumber:   sampleNumber,
				OrderIndex:     int(sampleNumber - 1),
				RawTimestamp:   raw,
				TimestampBits:  bits,
				Timescale:      tr.Mdhd.Timescale,
				RawNS:          rawNS,
				DurationNS:     durationNS,
				BoxOffset:      sttsOffset,
			})
			sampleNumber++
			cursor += uint64(entry.SampleDelta)
		}
	}
	return events, nil
}
