// Package pipeline wires the stages together: EBML read, timestamp repair,
// index rebuild, fan-out, per-layer RTP recovery, fan-in and reporting.
package pipeline

import (
	"time"

	"mediad/internal/ebml"
	"mediad/internal/fanin"
	"mediad/internal/fanout"
	"mediad/internal/mp4x"
	"mediad/internal/repair"
	"mediad/internal/report"
	"mediad/internal/rtp"
)

// Options configures one pipeline run.
type Options struct {
	Repair repair.Options
	Index  repair.IndexOptions
	// ReorderWindow is the RTP reorder window; capped at rtp.Window (64).
	ReorderWindow int
	// SlowConsumer delays the middle representation's consumer.
	SlowConsumer time.Duration
	// LayerTransport overrides the transport model per representation.
	LayerTransport map[string]fanout.Transport
	// CancelLayer cancels that representation's consumer after a third of
	// the stream, modelling an editor that walks away from one rendition.
	CancelLayer string
	// SwitchFraction places the planned representation switch, as a fraction
	// of the clip length. Default 0.5.
	SwitchFraction float64
	// LatencySamples caps retained publish-latency samples.
	LatencySamples int
}

func (o Options) withDefaults() Options {
	if o.ReorderWindow <= 0 || o.ReorderWindow > rtp.Window {
		o.ReorderWindow = rtp.Window
	}
	if o.SwitchFraction <= 0 || o.SwitchFraction >= 1 {
		o.SwitchFraction = 0.5
	}
	if o.LatencySamples <= 0 {
		o.LatencySamples = 512
	}
	return o
}

// Output is everything a run produced.
type Output struct {
	Summary     report.Summary
	Repair      *repair.Result
	Index       repair.IndexResult
	Merge       fanin.Result
	Ladder      fanout.Aggregate
	LadderStats []fanout.LayerStats
	RTP         rtp.Stats
	FileInfo    ebml.FileInfo
	CrossCheck  mp4x.CrossCheck
	// Damage is the input defect that stopped the read, or nil.
	Damage error
}
