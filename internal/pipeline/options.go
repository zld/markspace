package pipeline

import (
	"fmt"

	"mediapipeline/internal/track"
)

// Options tunes one run.
type Options struct {
	// Resume continues a previously interrupted run from its checkpoint.
	Resume bool
	// MappingFor builds the track mapping.  A nil value maps identities.
	MappingFor func([]track.Input) (track.Map, error)
}

func (o Options) mapFor(inputs []track.Input) (track.Map, error) {
	if o.MappingFor != nil {
		return o.MappingFor(inputs)
	}
	return track.Identity(inputs)
}

var _ = fmt.Sprintf
