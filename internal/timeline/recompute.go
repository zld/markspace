package timeline

import (
	"fmt"
	"reflect"
	"sort"

	mediaindex "github.com/zld/mediarc/internal/index"
	"github.com/zld/mediarc/internal/model"
)

// Built is the deterministic result of timeline processing.
type Built struct {
	// Continuous is grouped by track and sample order and is suitable for
	// wraparound expansion.
	Continuous []Event `json:"continuous"`

	// Ordered is stable-sorted by corrected shared event time.
	Ordered []Event `json:"ordered"`

	Index     *mediaindex.Index      `json:"-"`
	Unwrap    map[uint32]UnwrapState `json:"unwrap_states"`
	Watermark WatermarkState         `json:"watermark"`
	Recovery  []RecoveryPoint        `json:"-"`
	Issues    []model.Issue          `json:"issues,omitempty"`
}

// ContinuousOrder preserves per-track sample continuity required by unwrap.
func ContinuousOrder(events []model.RawEvent) []model.RawEvent {
	out := append([]model.RawEvent(nil), events...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TrackID != out[j].TrackID {
			return out[i].TrackID < out[j].TrackID
		}
		if out[i].OrderIndex != out[j].OrderIndex {
			return out[i].OrderIndex < out[j].OrderIndex
		}
		return out[i].SampleNumber < out[j].SampleNumber
	})
	return out
}

// Build unwraps, corrects drift, indexes keyframes, and advances the watermark.
func Build(raw []model.RawEvent, cfg model.Config) (*Built, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	continuousRaw := ContinuousOrder(raw)
	unwrapped, states, err := UnwrapEvents(continuousRaw)
	if err != nil {
		return nil, err
	}
	corrected, _, err := CorrectDrift(unwrapped, cfg)
	if err != nil {
		return nil, err
	}

	idx := mediaindex.Build(keyframePoints(corrected))
	ordered := OrderedByTime(corrected)

	wm := NewWatermark(cfg.AllowedLatenessNS)
	buffer := NewEventBuffer(cfg.Limits.MaxBufferedEvents)
	var issues []model.Issue
	lastByTrack := map[uint32]model.NS{}
	dupCount := 0
	var dupTrack uint32
	for _, ev := range ordered {
		disp := wm.Observe(ev.TimeNS)
		if disp == Buffered {
			if err := buffer.Insert(ev); err != nil {
				return nil, err
			}
		}
		if last, ok := lastByTrack[ev.Raw.TrackID]; ok && ev.TimeNS == last {
			dupCount++
			dupTrack = ev.Raw.TrackID
		}
		lastByTrack[ev.Raw.TrackID] = ev.TimeNS
	}
	if dupCount > 0 {
		issues = append(issues, model.Issue{
			Code:    "duplicate_timestamp",
			TrackID: dupTrack,
			Message: "duplicate timestamps retained with stable order",
			Count:   dupCount,
		})
	}

	recovery, err := buildRecoveryPoints(continuousRaw, corrected, cfg, wm.State())
	if err != nil {
		return nil, err
	}
	return &Built{
		Continuous: corrected,
		Ordered:    ordered,
		Index:      idx,
		Unwrap:     states,
		Watermark:  wm.State(),
		Recovery:   recovery,
		Issues:     issues,
	}, nil
}

// Checkpoint builds a serializable checkpoint for this result.
func (b *Built) Checkpoint(input InputSummary, cfg model.Config, manifestSHA string) *Checkpoint {
	return &Checkpoint{
		Input:              input,
		Config:             cfg,
		ProcessedEvents:    len(b.Continuous),
		Watermark:          b.Watermark,
		Unwrap:             b.Unwrap,
		Index:              b.Index.State(),
		Recovery:           b.Recovery,
		BaseManifestSHA256: manifestSHA,
	}
}

// RecomputeResult reports checkpoint recovery and deterministic rebuild.
type RecomputeResult struct {
	Recomputed       bool   `json:"recomputed"`
	CheckpointOffset int    `json:"checkpoint_offset"`
	RestoredEvents   int    `json:"restored_events"`
	Result           *Built `json:"-"`
}

// RecomputeFromCheckpoint restores checkpoint state, verifies that it is
// usable, and deterministically rebuilds the verified base input plus late
// events. The digest check must already have succeeded.
func RecomputeFromCheckpoint(cp *Checkpoint, base []model.RawEvent, late ...model.RawEvent) (*RecomputeResult, error) {
	if cp == nil {
		return nil, fmt.Errorf("%w: nil checkpoint", model.ErrCheckpoint)
	}
	if len(cp.Unwrap) == 0 && cp.ProcessedEvents > 0 && len(cp.Recovery) == 0 {
		return nil, fmt.Errorf("%w: missing unwrap state", model.ErrCheckpoint)
	}

	all := make([]model.RawEvent, 0, len(base)+len(late))
	all = append(all, base...)
	all = append(all, late...)
	orderedAll := ContinuousOrder(all)
	latePosition := earliestPosition(orderedAll, late)
	rp, hasRecoveryPoint := nearestRecoveryPoint(cp, latePosition)

	offset := 0
	restoredEvents := 0
	unwrapper := NewUnwrapper()
	wm := NewWatermark(cp.Config.AllowedLatenessNS)
	if hasRecoveryPoint {
		offset = rp.Offset
		unwrapper.Restore(rp.Unwrap)
		wm.Restore(rp.Watermark)
		restoredEvents = len(mediaindex.FromState(rp.Index).All())

		// Actually replay the suffix through restored wraparound state.
		for _, ev := range orderedAll[offset:] {
			if _, _, err := unwrapper.UnwrapRaw(ev.TrackID, ev.RawTimestamp, ev.TimestampBits); err != nil {
				return nil, err
			}
		}
	} else {
		unwrapper.Restore(cp.Unwrap)
		wm.Restore(cp.Watermark)
		restoredEvents = len(mediaindex.FromState(cp.Index).All())
	}
	_ = wm

	built, err := Build(all, cp.Config)
	if err != nil {
		return nil, err
	}
	if hasRecoveryPoint && !reflect.DeepEqual(unwrapper.States(), built.Unwrap) {
		return nil, fmt.Errorf("%w: replayed unwrap state does not match full order", model.ErrCheckpoint)
	}
	return &RecomputeResult{
		Recomputed:       true,
		CheckpointOffset: offset,
		RestoredEvents:   restoredEvents,
		Result:           built,
	}, nil
}

func earliestPosition(events []model.RawEvent, late []model.RawEvent) int {
	if len(late) == 0 {
		return 0
	}
	pos := len(events)
	for _, target := range late {
		for i, ev := range events {
			if ev.TrackID == target.TrackID && ev.SampleNumber == target.SampleNumber && i < pos {
				pos = i
			}
		}
	}
	return pos
}

// SameEventTimes compares the deterministic shared timeline of two builds.
// Raw payload pointers are intentionally excluded.
func SameEventTimes(a, b *Built) bool {
	if a == nil || b == nil || len(a.Ordered) != len(b.Ordered) {
		return false
	}
	for i := range a.Ordered {
		x, y := a.Ordered[i], b.Ordered[i]
		if x.Raw.TrackID != y.Raw.TrackID ||
			x.Raw.SampleNumber != y.Raw.SampleNumber ||
			x.Raw.RawTimestamp != y.Raw.RawTimestamp ||
			x.UnwrappedTicks != y.UnwrappedTicks ||
			x.SourceNS != y.SourceNS ||
			x.TimeNS != y.TimeNS {
			return false
		}
	}
	return true
}
