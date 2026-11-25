// Package pipeline composes the EBML, track, timeline, fragment and RTP
// kernels into one deterministic report. The CLI only parses arguments and
// prints this report.
package pipeline

import (
	"mediacheck/internal/ebml"
	"mediacheck/internal/fragment"
	"mediacheck/internal/report"
	"mediacheck/internal/sample"
	"mediacheck/internal/tracks"
)

// ValidateBundle runs every local subsystem and accumulates structured records.
// Structural EBML failures stop later container-dependent stages; validation
// conflicts are accumulated so all stable locations can be reported.
func ValidateBundle(b *sample.Bundle) *report.Report {
	rep := &report.Report{Records: []report.Record{}}

	doc, perr := ebml.ParseDocument(b.EBML)
	if perr != nil {
		rep.Records = append(rep.Records, perr.Record)
		return rep
	}

	var graph *tracks.Graph
	built, gerr := tracks.BuildGraph(b.EBML, doc)
	if gerr != nil {
		rep.Records = append(rep.Records, gerr.Record)
	} else {
		graph = built
		rep.Records = append(rep.Records, graph.Validate()...)
	}

	streams, rerr := b.Streams()
	if rerr != nil {
		rep.Records = append(rep.Records, rerr.Record)
	}

	fragResult, ferr := fragment.Check(doc, graph, streams)
	if ferr != nil {
		rep.Records = append(rep.Records, ferr.Record)
		return rep
	}
	rep.Records = append(rep.Records, fragResult.Records...)
	return rep
}
