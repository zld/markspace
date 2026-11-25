package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"mediacore/internal/audit"
	"mediacore/internal/clock"
	"mediacore/internal/demux"
	"mediacore/internal/maptrack"
	"mediacore/internal/merge"
	"mediacore/internal/model"
	"mediacore/internal/remux"
)

// ErrInterrupted marks a deliberate stop after StopAfter (recovery tests).
var ErrInterrupted = errors.New("pipeline interrupted after stage")

// RulesFunc derives the explicit mapping rule set from demuxed tracks.
type RulesFunc func(tracks []model.Track) []maptrack.Rule

// Pipeline runs demux -> map -> remux -> manifest for one input file,
// checkpointing every stage in the store.
type Pipeline struct {
	Store     *Store
	OutDir    string
	Threshold int64 // drift failure threshold, unified ticks
	Tolerance int64 // audit time tolerance, unified ticks
	Outs      []model.OutputTrack
	RulesFn   RulesFunc
	StopAfter model.Stage // test hook: interrupt after this stage
}

// StageNote records how one stage completed.
type StageNote struct {
	Stage  model.Stage `json:"stage"`
	Status string      `json:"status"` // done | resumed | redone
}

// Result is one sample's pipeline outcome.
type Result struct {
	TaskKey  string              `json:"task_key"`
	SampleID string              `json:"sample_id"`
	OutPath  string              `json:"out_path"`
	Events   []model.Event       `json:"-"`
	Drift    []clock.DriftReport `json:"drift,omitempty"`
	Report   audit.Report        `json:"report"`
	Notes    []StageNote         `json:"stages"`
}

// TaskKey derives a stable task key from sample id and content.
func TaskKey(sampleID string, content []byte) string {
	sum := sha256.Sum256(content)
	return fmt.Sprintf("%s-%s", sampleID, hex.EncodeToString(sum[:8]))
}

type demuxPayload struct {
	Tracks []model.Track `json:"tracks"`
	Events []model.Event `json:"events"`
}

type mapPayload struct {
	Events []model.Event       `json:"events"`
	Drift  []clock.DriftReport `json:"drift"`
	Shift  int64               `json:"shift"`
}

type remuxPayload struct {
	Events  []model.Event `json:"events"`
	OutPath string        `json:"out_path"`
}

func noteFor(r RunResult, err error) string {
	if r.Resumed {
		return "resumed"
	}
	if r.Redone {
		return "redone"
	}
	return "done"
}

// deriveDurations fills zero durations from the delta to the next event
// on the same track (in tick order); the last event inherits the previous
// delta.
func deriveDurations(events []model.Event) {
	byTrack := map[string][]int{}
	for i := range events {
		if events[i].Kind == model.EventMedia {
			byTrack[events[i].TrackID] = append(byTrack[events[i].TrackID], i)
		}
	}
	for _, idxs := range byTrack {
		allZero := true
		for _, i := range idxs {
			if events[i].Duration != 0 {
				allZero = false
			}
		}
		if !allZero || len(idxs) < 2 {
			continue
		}
		// idxs follow demux order, which is presentation order here.
		for k := 0; k+1 < len(idxs); k++ {
			events[idxs[k]].Duration = events[idxs[k+1]].Ticks - events[idxs[k]].Ticks
		}
		events[idxs[len(idxs)-1]].Duration = events[idxs[len(idxs)-2]].Duration
	}
}

// Run executes the pipeline for one input. kind is "mp4" or "rtp".
func (p *Pipeline) Run(sampleID, path, kind string) (*Result, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, model.NewError(model.ErrIO, model.StageDemux, "", -1, "read input: %v", err)
	}
	key := TaskKey(sampleID, raw)
	if err := os.MkdirAll(p.OutDir, 0o755); err != nil {
		return nil, model.NewError(model.ErrIO, model.StageRemux, "", -1, "mkdir out: %v", err)
	}
	res := &Result{TaskKey: key, SampleID: sampleID}
	note := func(stage model.Stage, r RunResult) {
		res.Notes = append(res.Notes, StageNote{Stage: stage, Status: noteFor(r, nil)})
	}
	// Stage 1: demux.
	dr, err := p.Store.RunStage(key, model.StageDemux, func() ([]byte, error) {
		var tracks []model.Track
		var events []model.Event
		var derr error
		switch kind {
		case "mp4":
			tracks, events, derr = demux.DemuxMP4(path, demux.Options{})
		case "rtp":
			tracks, events, derr = demux.DemuxRTP(path, demux.Options{})
		default:
			return nil, model.NewError(model.ErrUnsupported, model.StageDemux, "", -1, "kind %q", kind)
		}
		if derr != nil {
			return nil, derr
		}
		return json.Marshal(demuxPayload{Tracks: tracks, Events: events})
	})
	if err != nil {
		return res, err
	}
	note(model.StageDemux, dr)
	var dp demuxPayload
	if err := json.Unmarshal(dr.Payload, &dp); err != nil {
		return res, model.NewError(model.ErrCheckpoint, model.StageDemux, "", -1, "decode: %v", err)
	}
	if p.StopAfter == model.StageDemux {
		return res, ErrInterrupted
	}
	// Stage 2: calibrate + map.
	mr, err := p.Store.RunStage(key, model.StageMap, func() ([]byte, error) {
		drift, err := maptrack.CalibrateTracks(dp.Events, dp.Tracks, p.Threshold)
		if err != nil {
			return nil, err
		}
		// Uniform shift so the earliest tick is zero (RTP absolute
		// timestamps become a zero-based presentation timeline).
		var shift int64
		first := true
		for _, e := range dp.Events {
			if first || e.Ticks < shift {
				shift = e.Ticks
				first = false
			}
		}
		for i := range dp.Events {
			dp.Events[i].Ticks -= shift
		}
		// Tracks with no duration information (RTP dumps) get durations
		// derived from successive presentation times, never from a fixed
		// frame interval.
		deriveDurations(dp.Events)
		events, err := maptrack.Apply(dp.Events, dp.Tracks, p.RulesFn(dp.Tracks), p.Outs)
		if err != nil {
			return nil, err
		}
		return json.Marshal(mapPayload{Events: events, Drift: drift, Shift: shift})
	})
	if err != nil {
		return res, err
	}
	note(model.StageMap, mr)
	var mp mapPayload
	if err := json.Unmarshal(mr.Payload, &mp); err != nil {
		return res, model.NewError(model.ErrCheckpoint, model.StageMap, "", -1, "decode: %v", err)
	}
	res.Drift = mp.Drift
	if p.StopAfter == model.StageMap {
		return res, ErrInterrupted
	}
	// Stage 3: remux (payload bytes copied verbatim).
	outPath := filepath.Join(p.OutDir, key+".mp4")
	rr, err := p.Store.RunStage(key, model.StageRemux, func() ([]byte, error) {
		events := make([]model.Event, len(mp.Events))
		copy(events, mp.Events)
		if err := remux.WriteMP4(outPath, p.Outs, events); err != nil {
			return nil, err
		}
		return json.Marshal(remuxPayload{Events: events, OutPath: outPath})
	})
	if err != nil {
		return res, err
	}
	note(model.StageRemux, rr)
	var rp remuxPayload
	if err := json.Unmarshal(rr.Payload, &rp); err != nil {
		return res, model.NewError(model.ErrCheckpoint, model.StageRemux, "", -1, "decode: %v", err)
	}
	res.Events = rp.Events
	res.OutPath = rp.OutPath
	if p.StopAfter == model.StageRemux {
		return res, ErrInterrupted
	}
	// Stage 4: manifest for this task.
	mfr, err := p.Store.RunStage(key, model.StageManifest, func() ([]byte, error) {
		m := merge.Build([]merge.TaskResult{{TaskKey: key, SampleID: sampleID, Events: rp.Events}})
		return m.Marshal(), nil
	})
	if err != nil {
		return res, err
	}
	note(model.StageManifest, mfr)
	var man merge.Manifest
	if err := json.Unmarshal(mfr.Payload, &man); err != nil {
		return res, model.NewError(model.ErrCheckpoint, model.StageManifest, "", -1, "decode: %v", err)
	}
	// Audit: re-demux the output and reconcile against mapped events.
	_, outEvents, err := demux.DemuxMP4(rp.OutPath, demux.Options{})
	if err != nil {
		return res, err
	}
	res.Report = audit.Compare(sampleID, key, mp.Events, outEvents, p.Tolerance, mp.Drift)
	return res, nil
}
