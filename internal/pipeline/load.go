package pipeline

import (
	"fmt"

	"mediasync/internal/ingest"
	"mediasync/internal/media"
	"mediasync/internal/timeline"
)

type recordCounter interface{ recordCount() int }

type opusTrack struct{ result *ingest.OpusResult }

func (o opusTrack) recordCount() int { return o.result.Stats.Datagrams }

type aacTrack struct{ result *ingest.AACResult }

func (a aacTrack) recordCount() int { return len(a.result.Samples) }

type subtitleTrack struct{ file *ingest.SubtitleFile }

func (s subtitleTrack) recordCount() int { return len(s.file.Cues) }

type loaded struct {
	spec  ingest.TrackSpec
	track recordCounter
	input timeline.Input
}

func loadTrack(spec ingest.TrackSpec, inputDir string) (loaded, error) {
	lt := loaded{spec: spec}
	switch spec.Kind {
	case ingest.KindOpus:
		r, err := ingest.ReadOpusTrack(spec, inputDir)
		if err != nil {
			return lt, err
		}
		lt.track = opusTrack{r}
		lt.input = timeline.Input{Spec: spec, Opus: r}
	case ingest.KindAAC:
		r, err := ingest.ReadAACTrack(spec, inputDir)
		if err != nil {
			return lt, err
		}
		lt.track = aacTrack{r}
		lt.input = timeline.Input{Spec: spec, AAC: r}
	case ingest.KindSubtitle:
		path, err := ingest.TrackPath(inputDir, spec.Path)
		if err != nil {
			return lt, err
		}
		sf, err := ingest.LoadSubtitles(path, spec.ID)
		if err != nil {
			return lt, err
		}
		lt.track = subtitleTrack{sf}
		lt.input = timeline.Input{Spec: spec, Subtitles: sf}
	default:
		return lt, media.InputError("ingest.manifest", spec.ID, fmt.Errorf("unsupported kind %q", spec.Kind))
	}
	return lt, nil
}
