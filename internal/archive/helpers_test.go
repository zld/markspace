package archive

import (
	"errors"

	"mediaarchive/internal/ebml"
	"mediaarchive/internal/pipeline"
)

// toEvents converts EBML block events into pipeline events for re-encoding.
func toEvents(blockEvents []ebml.BlockEvent) []pipeline.Event {
	out := make([]pipeline.Event, len(blockEvents))
	for i, ev := range blockEvents {
		out[i] = pipeline.Event{
			Track:     ev.Track,
			TimeNanos: ev.TimeNanos,
			SourceSeq: ev.SourceSeq,
			Payload:   ev.Payload,
		}
	}
	return out
}

// timelineErrIs asserts an error wraps the given shared sentinel.
func timelineErrIs(err error, target error) bool {
	return err != nil && errors.Is(err, target)
}
