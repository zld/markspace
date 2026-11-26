package render

import (
	mp4scan "replayctl/internal/mp4"
	"replayctl/internal/timebase"
	"replayctl/internal/timeline"
)

// Renderer resolves source paths to opened indexed media.
type Renderer struct {
	Sources map[string]*Source
}

// New creates a renderer.
func New() *Renderer { return &Renderer{Sources: map[string]*Source{}} }

// AddSource registers an already-opened source.
func (r *Renderer) AddSource(s *Source) { r.Sources[s.Path] = s }

// OpenAndAdd opens and registers path.
func (r *Renderer) OpenAndAdd(path string) (*Source, error) {
	if s, ok := r.Sources[path]; ok {
		return s, nil
	}
	s, err := OpenSource(path)
	if err != nil {
		return nil, err
	}
	r.Sources[path] = s
	return s, nil
}

// Artifact contains mux-ready tracks and render-time alignment metrics.
type Artifact struct {
	Video                     mp4scan.TrackIn
	Audio                     mp4scan.TrackIn
	Frames                    int
	Pixels                    int64
	AudioSamples              int
	Peak                      int16
	VideoAlignErrorMS         int64
	AudioBoundaryErrorSamples int64
}

type tickClip struct {
	clip     timeline.Clip
	start    int64
	end      int64
	sourceIn int64
}

func clipTicks(c timeline.Clip, rate uint32) (tickClip, error) {
	start, err := timebase.Ms(c.StartMS).Floor(rate)
	if err != nil {
		return tickClip{}, err
	}
	end, err := timebase.Ms(c.EndMS).Floor(rate)
	if err != nil {
		return tickClip{}, err
	}
	in, err := timebase.Ms(c.SourceInMS).Floor(rate)
	if err != nil {
		return tickClip{}, err
	}
	return tickClip{clip: c, start: start, end: end, sourceIn: in}, nil
}

func (r *Renderer) sourceFor(c timeline.Clip) (*Source, error) {
	return r.OpenAndAdd(c.SourcePath)
}
