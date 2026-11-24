package remux

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"mediarc/internal/demux"
	"mediarc/internal/model"
	"mediarc/internal/timeline"
	"mediarc/internal/track"
)

type Result struct {
	Archive     *model.Archive `json:"archive"`
	Bytes       []byte         `json:"-"`
	SHA256      string         `json:"sha256"`
	SampleCount int            `json:"sample_count"`
}

var supportedCodecs = map[model.Codec]bool{
	model.CodecSyntheticVideo: true,
	model.CodecSyntheticAudio: true,
	model.CodecWebVTT:         true,
	model.CodecChapter:        true,
	model.CodecID3:            true,
	model.CodecEmsg:           true,
	model.CodecSEI:            true,
}

// Remux performs a lossless synthetic container transpackage. It copies
// encoded payloads and integer timestamps exactly. It rejects property
// changes or ambiguous color metadata rather than approximating them.
func Remux(in *model.Archive, plan *track.Plan, tl *timeline.Result) (*Result, error) {
	if in == nil || plan == nil || len(plan.Mappings) == 0 {
		return nil, model.NewError(model.StageRemux, model.CodeInvalidArgument, "missing archive or mapping plan", nil)
	}
	bySource := map[uint32]track.Mapping{}
	out := &model.Archive{Brand: in.Brand, GeneratedBy: in.GeneratedBy + " remux=lossless"}

	for _, m := range plan.Mappings {
		bySource[m.SourceID] = m
		tr := m.Source
		tr.ID = m.TargetID
		if !supportedCodecs[tr.Codec] {
			err := model.NewError(model.StageRemux, model.CodeNonLossless,
				fmt.Sprintf("codec %s has no lossless output representation", tr.Codec), nil)
			return nil, model.AtTrack(err, m.SourceID)
		}
		if tr.Type == model.TrackAudio {
			if tr.SampleRate <= 0 || tr.Channels <= 0 || tr.Layout == "" {
				err := model.NewError(model.StageRemux, model.CodeNonLossless,
					"audio track lacks explicit sample rate/channel layout", nil)
				return nil, model.AtTrack(err, m.SourceID)
			}
			if expected := model.LayoutChannels(tr.Layout); expected != tr.Channels {
				err := model.NewError(model.StageRemux, model.CodeLayoutMismatch,
					fmt.Sprintf("layout %s implies %d channels but track declares %d", tr.Layout, expected, tr.Channels), nil)
				return nil, model.AtTrack(err, m.SourceID)
			}
		}
		if fields := trackColorConflicts(tr); len(fields) > 0 {
			err := model.NewError(model.StageRemux, model.CodeColorConflict,
				"track has conflicting source color metadata; single-value muxing refused", nil)
			err.Fields = fields
			return nil, model.AtTrack(err, m.SourceID)
		}
		out.Tracks = append(out.Tracks, tr)
	}

	for _, s := range in.Samples {
		m, ok := bySource[s.TrackID]
		if !ok {
			continue
		}
		cp := s
		cp.TrackID = m.TargetID
		cp.Payload = append([]byte(nil), s.Payload...)
		out.Samples = append(out.Samples, cp)
	}

	orderedEvents := orderEvents(in.Events, tl)
	for _, ev := range orderedEvents {
		m, ok := bySource[ev.TrackID]
		if !ok {
			continue
		}
		cp := ev
		cp.TrackID = m.TargetID
		cp.Payload = append([]byte(nil), ev.Payload...)
		out.Events = append(out.Events, cp)
	}

	raw, err := demux.EncodeContainer(out)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	return &Result{Archive: out, Bytes: raw, SHA256: hex.EncodeToString(sum[:]), SampleCount: len(out.Samples)}, nil
}

func trackColorConflicts(tr model.Track) []string {
	values := make([]model.ColorMeta, 0, 1+len(tr.ColorAlternatives))
	if tr.Color != nil {
		values = append(values, *tr.Color)
	}
	values = append(values, tr.ColorAlternatives...)
	seen := map[string]bool{}
	var fields []string
	for i := 0; i < len(values); i++ {
		for j := i + 1; j < len(values); j++ {
			for _, f := range values[i].ConflictFields(values[j]) {
				if !seen[f] {
					seen[f] = true
					fields = append(fields, f)
				}
			}
		}
	}
	return fields
}

func orderEvents(events []model.TimedEvent, tl *timeline.Result) []model.TimedEvent {
	if tl == nil || len(tl.OrderedIDs) == 0 {
		return append([]model.TimedEvent(nil), events...)
	}
	byID := map[string]model.TimedEvent{}
	for _, ev := range events {
		byID[ev.ID] = ev
	}
	var out []model.TimedEvent
	used := map[string]bool{}
	for _, id := range tl.OrderedIDs {
		if ev, ok := byID[id]; ok {
			out = append(out, ev)
			used[id] = true
		}
	}
	for _, ev := range events {
		if !used[ev.ID] {
			out = append(out, ev)
		}
	}
	return out
}
