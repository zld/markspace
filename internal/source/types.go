package source

import "mediaprep/internal/model"

// Scope identifies where a metadata value was found. Lower Rank wins.
type Scope string

const (
	ScopeSampleOrDescriptor Scope = "sample-description-or-mxf-descriptor"
	ScopeTrack              Scope = "track-description"
	ScopeContainer          Scope = "container-description"
)

func (s Scope) Rank() int {
	switch s {
	case ScopeSampleOrDescriptor:
		return 0
	case ScopeTrack:
		return 1
	case ScopeContainer:
		return 2
	default:
		return 3
	}
}

// RawField is one uncoalesced metadata assertion. Keeping every assertion is
// what allows conflict reports to retain original values and source offsets.
type RawField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Scope  Scope  `json:"scope"`
	Track  string `json:"track"`
	Offset int64  `json:"offset"`
}

type Sample struct {
	Index     int    `json:"index"`
	Offset    int64  `json:"offset"`
	Size      int    `json:"size"`
	DTS       int64  `json:"dts"`
	PTS       int64  `json:"pts"`
	Duration  int64  `json:"duration"`
	KeyFrame  bool   `json:"key_frame"`
	Payload   []byte `json:"-"`
}

type PCMFormat struct {
	Channels   int `json:"channels"`
	Bits       int `json:"bits"`
	SampleRate int `json:"sample_rate"`
}

type Track struct {
	Index      int         `json:"index"`
	ID         int         `json:"id"`
	Label      string      `json:"label"`
	Role       string      `json:"role"`
	Codec      string      `json:"codec"`
	Clock      model.Clock `json:"clock"`
	Duration   int64       `json:"duration"`
	Width      int         `json:"width,omitempty"`
	Height     int         `json:"height,omitempty"`
	PCM        *PCMFormat  `json:"pcm,omitempty"`
	Samples    []Sample    `json:"samples"`
	RawFields  []RawField  `json:"raw_fields,omitempty"`
}

type MediaFile struct {
	Path            string     `json:"path"`
	Format          string     `json:"format"`
	Size            int64      `json:"size"`
	Tracks          []*Track   `json:"tracks"`
	ContainerFields []RawField `json:"container_fields,omitempty"`
}

func (m *MediaFile) AllRawFields() []RawField {
	out := append([]RawField{}, m.ContainerFields...)
	for _, t := range m.Tracks {
		out = append(out, t.RawFields...)
	}
	return out
}

func (m *MediaFile) TrackByLabel(label string) *Track {
	for _, t := range m.Tracks {
		if t.Label == label {
			return t
		}
	}
	return nil
}
