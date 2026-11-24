package timeline

import (
	mediaindex "github.com/zld/mediarc/internal/index"
	"github.com/zld/mediarc/internal/model"
)

const recoveryIntervalEvents = 8

func keyframePoints(events []Event) []mediaindex.Keyframe {
	points := make([]mediaindex.Keyframe, 0)
	for _, ev := range events {
		if ev.Raw.Keyframe {
			points = append(points, mediaindex.Keyframe{
				TrackID:        ev.Raw.TrackID,
				SampleNumber:   ev.Raw.SampleNumber,
				RawTimestamp:   ev.Raw.RawTimestamp,
				Timescale:      ev.Raw.Timescale,
				TimeNS:         ev.TimeNS,
				Offset:         ev.Raw.BoxOffset,
				Representation: ev.Raw.Representation,
			})
		}
	}
	return points
}

func buildRecoveryPoints(raw []model.RawEvent, corrected []Event, cfg model.Config, finalWM WatermarkState) ([]RecoveryPoint, error) {
	points := make([]RecoveryPoint, 0, len(raw)/recoveryIntervalEvents+1)
	for offset := 0; offset <= len(raw); {
		_, states, err := UnwrapEvents(raw[:offset])
		if err != nil {
			return nil, err
		}

		var wmState WatermarkState
		if offset == len(raw) {
			wmState = finalWM
		} else {
			wm := NewWatermark(cfg.AllowedLatenessNS)
			for _, ev := range corrected[:offset] {
				wm.Observe(ev.TimeNS)
			}
			wmState = wm.State()
		}

		idx := mediaindex.Build(keyframePoints(corrected[:offset]))
		points = append(points, RecoveryPoint{
			Offset:    offset,
			Watermark: wmState,
			Unwrap:    states,
			Index:     idx.State(),
		})

		if offset == len(raw) {
			break
		}
		offset += recoveryIntervalEvents
		if offset > len(raw) {
			offset = len(raw)
		}
	}
	return points, nil
}

func nearestRecoveryPoint(cp *Checkpoint, latePosition int) (RecoveryPoint, bool) {
	if cp == nil || len(cp.Recovery) == 0 {
		return RecoveryPoint{}, false
	}
	var best *RecoveryPoint
	for i := range cp.Recovery {
		rp := &cp.Recovery[i]
		if rp.Offset > latePosition {
			continue
		}
		if best == nil || rp.Offset > best.Offset {
			best = rp
		}
	}
	if best == nil {
		return RecoveryPoint{}, false
	}
	return *best, true
}
