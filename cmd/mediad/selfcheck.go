package main

import (
	"fmt"
	"math"

	"mediad/internal/model"
	"mediad/internal/timeline"
)

// runSelfCheck exercises the two structural failure modes that have no
// fixture capture: a cyclic track dependency graph and an integer time-base
// overflow. Both must print a stable code and terminate with status 2.
func runSelfCheck(kind string) error {
	switch kind {
	case "graph-cycle":
		g := &timeline.Graph{
			Nodes: []timeline.Node{
				{ID: "a1", Kind: timeline.KindAudio},
				{ID: "a2", Kind: timeline.KindAudio},
				{ID: "m", Kind: timeline.KindManifest},
			},
			Edges: []timeline.Edge{
				{From: "a1", To: "a2"}, {From: "a2", To: "a1"},
				{From: "a1", To: "m"}, {From: "a2", To: "m"},
			},
		}
		if _, err := g.Validate(); err != nil {
			return err
		}
		return model.ErrGraphCycle.At("a1", 0, "graph", 0, "self-check inconsistency")
	case "time-overflow":
		// A tick count that cannot be represented in nanoseconds as int64.
		if _, err := model.ToNano(math.MaxInt64, 1); err != nil {
			return err
		}
		return model.ErrTimeOverflow.At("time", 0, "ingest", 0, "self-check inconsistency")
	default:
		return model.ErrUsage.At("", 0, "cli", 0,
			"--selfcheck expects graph-cycle or time-overflow")
	}
}

var _ = fmt.Sprint
