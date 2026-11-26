// Package pipeline wires parsing, mapping, rendering, validation, and journal recovery.
package pipeline

import (
	"path/filepath"

	"replayctl/internal/event"
	"replayctl/internal/fault"
	"replayctl/internal/index"
	"replayctl/internal/journal"
	mp4scan "replayctl/internal/mp4"
	"replayctl/internal/render"
	"replayctl/internal/timeline"
	"replayctl/internal/validate"
)

// Input is the fully-resolved pipeline request.
type Input struct {
	Out              string
	MediaPath        string
	EventPath        string
	EventID          string
	StartMS          int64
	EndMS            int64
	Timeline         timeline.Timeline
	CrashAfterRename bool
}

// Result contains terminal counters and verified delivery metadata.
type Result struct {
	DeliveryKey string
	Artifact    string
	SHA256      string
	Metrics     *validate.Metrics
	Recovered   int
	Published   int
	Duplicate   int
	Committed   bool
}

// Run executes one idempotent delivery request.
func Run(in Input) (*Result, error) {
	// Parsing and indexing happen before the output directory is created.
	mov, err := mp4scan.ScanFile(in.MediaPath)
	if err != nil {
		return nil, err
	}
	if _, err := index.Build(mov); err != nil {
		return nil, err
	}
	if err := validateSupportedFormats(mov); err != nil {
		return nil, err
	}

	ev, err := selectEvent(in.EventPath, in.EventID)
	if err != nil {
		return nil, err
	}

	if _, err := in.Timeline.MapWindow(in.StartMS, in.EndMS); err != nil {
		return nil, fault.Wrap(err, fault.TimeGap, fault.StageTimeline, in.EventPath, ev.RawOffset, ev.Tick)
	}

	r := render.New()
	defer func() {
		for _, src := range r.Sources {
			_ = src.Close()
		}
	}()
	art, err := r.Render(in.Timeline, in.StartMS, in.EndMS)
	if err != nil {
		if fe, ok := fault.From(err); ok && fe.Code == fault.TimeGap {
			return nil, fault.Wrap(fe, fault.TimeGap, fault.StageTimeline, in.EventPath, ev.RawOffset, ev.Tick)
		}
		return nil, err
	}

	key := journal.DeliveryKey(ev.ID, in.StartMS, in.EndMS)
	return publish(in, key, ev.ID, art)
}

func validateSupportedFormats(mov *mp4scan.Movie) error {
	vt := mov.Track("video")
	at := mov.Track("audio")
	if vt == nil || at == nil {
		return fault.New(fault.Unsupported, fault.StageScan, mov.Path, -1, -1, "requires one video and one audio track")
	}
	if vt.Codec != "jpeg" {
		return fault.New(fault.Unsupported, fault.StageScan, mov.Path, -1, -1, "video codec %q is outside MJPEG support", vt.Codec)
	}
	if at.Codec != "sowt" {
		return fault.New(fault.Unsupported, fault.StageScan, mov.Path, -1, -1, "audio codec %q is outside little-endian PCM support", at.Codec)
	}
	if at.Timescale != 48_000 {
		return fault.New(fault.Unsupported, fault.StageScan, mov.Path, -1, -1, "audio timescale %d is not 48 kHz", at.Timescale)
	}
	return nil
}

func selectEvent(path, wantID string) (*event.Event, error) {
	events, err := event.ReadFile(path)
	if err != nil {
		return nil, err
	}
	for i := len(events) - 1; i >= 0; i-- {
		if wantID == "" || events[i].ID == wantID {
			return &events[i], nil
		}
	}
	return nil, fault.New(fault.EventParse, fault.StageEvent, path, -1, -1, "event %q not found", wantID)
}

func artifactRelative(key string) string {
	return filepath.ToSlash(filepath.Join("deliveries", journal.ArtifactName(key)))
}
