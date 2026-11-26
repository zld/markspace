package cue

import (
	"bytes"
	"encoding/json"

	"github.com/abema/go-mp4"

	"continuity/internal/media"
)

// SwitchRecord is the JSON payload carried by a generated emsg box.
type SwitchRecord struct {
	Type                 string `json:"type"`
	NewSource            string `json:"new_source"`
	RawAnchorNS          int64  `json:"raw_anchor_ns"`
	ContinuousAnchorNS   int64  `json:"continuous_anchor_ns"`
	MainLastContinuousNS int64  `json:"main_last_continuous_ns,omitempty"`
	BackupFirstRawNS     int64  `json:"backup_first_raw_ns,omitempty"`
}

// SwitchHint is an emsg hint converted to the project nanosecond clock.
type SwitchHint struct {
	Offset            int64        `json:"offset"`
	Version           uint8        `json:"version"`
	SchemeIDURI       string       `json:"scheme_id_uri"`
	Value             string       `json:"value"`
	EventID           uint32       `json:"event_id"`
	Timescale         uint32       `json:"timescale"`
	PresentationTicks uint64       `json:"presentation_ticks"`
	PresentationNS    int64        `json:"presentation_ns"`
	DurationTicks     uint32       `json:"duration_ticks"`
	DurationNS        int64        `json:"duration_ns"`
	Record            SwitchRecord `json:"record"`
	RawMessage        []byte       `json:"-"`
}

// ReadEMSG parses all top-level emsg boxes in an fMP4 segment. The go-mp4
// dependency owns ISOBMFF box decoding; this function only converts units and
// the project-specific JSON switch payload.
func ReadEMSG(data []byte) ([]SwitchHint, error) {
	var hints []SwitchHint
	r := bytes.NewReader(data)
	_, err := mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (interface{}, error) {
		if h.BoxInfo.Type != mp4.BoxTypeEmsg() {
			return nil, nil
		}
		box, _, err := h.ReadPayload()
		if err != nil {
			return nil, media.NewError(media.StageCue, int64(h.BoxInfo.Offset), "emsg", err).
				WithKind("mp4_payload")
		}
		emsg, ok := box.(*mp4.Emsg)
		if !ok {
			return nil, media.NewError(media.StageCue, int64(h.BoxInfo.Offset), "emsg", media.ErrInvalidVINT).
				WithKind("unexpected_emsg_type")
		}

		ticks := uint64(emsg.PresentationTimeDelta)
		if emsg.Version == 1 {
			ticks = emsg.PresentationTime
		}
		presentationNS, err := media.ScaleTicks(ticks, emsg.Timescale)
		if err != nil {
			return nil, media.NewError(media.StageCue, int64(h.BoxInfo.Offset), "emsg", err)
		}
		durationNS, err := media.ScaleTicks(uint64(emsg.EventDuration), emsg.Timescale)
		if err != nil {
			return nil, media.NewError(media.StageCue, int64(h.BoxInfo.Offset), "emsg", err)
		}

		hint := SwitchHint{
			Offset:            int64(h.BoxInfo.Offset),
			Version:           emsg.Version,
			SchemeIDURI:       emsg.SchemeIdUri,
			Value:             emsg.Value,
			EventID:           emsg.Id,
			Timescale:         emsg.Timescale,
			PresentationTicks: ticks,
			PresentationNS:    presentationNS,
			DurationTicks:     emsg.EventDuration,
			DurationNS:        durationNS,
			RawMessage:        append([]byte(nil), emsg.MessageData...),
		}
		if len(emsg.MessageData) > 0 {
			if err := json.Unmarshal(emsg.MessageData, &hint.Record); err != nil {
				return nil, media.NewError(media.StageCue, int64(h.BoxInfo.Offset), "emsg/message_data", err).
					WithKind("switch_record_json")
			}
		}
		hints = append(hints, hint)
		return nil, nil
	})
	if err != nil {
		if me, ok := media.AsError(err); ok {
			return hints, me
		}
		return hints, media.NewError(media.StageCue, -1, "mp4", err)
	}
	return hints, nil
}

// SourceClock converts an emsg switch record into a timeline clock mapping.
func (h SwitchHint) SourceClock() media.SourceClock {
	return media.SourceClock{
		SourceID:           h.Record.NewSource,
		RawAnchorNS:        h.Record.RawAnchorNS,
		ContinuousAnchorNS: h.Record.ContinuousAnchorNS,
	}
}

// Event represents the emsg as a cue event on the unified timeline.
func (h SwitchHint) Event() media.Event {
	return media.Event{
		Kind:       media.KindCue,
		PTSNS:      h.PresentationNS,
		DurationNS: h.DurationNS,
		EndNS:      h.PresentationNS + h.DurationNS,
		SourceID:   h.Record.NewSource,
		Offset:     h.Offset,
		Detail:     h.SchemeIDURI + "/" + h.Record.Type,
	}
}
