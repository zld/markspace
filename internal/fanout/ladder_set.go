// Package fanout distributes repaired blocks to several bitrate
// representations with independent recovery state. This file holds the
// aggregate Ladder type.
package fanout

import (
	"context"
	"time"

	"mediad/internal/media"
	"mediad/internal/merr"
)

// Ladder fans blocks out to ordered representations.
type Ladder struct {
	layers []*Layer
}

// NewLadder creates one layer per options entry.
func NewLadder(specs []LayerOptions) *Ladder {
	layers := make([]*Layer, 0, len(specs))
	for _, s := range specs {
		layers = append(layers, NewLayer(s))
	}
	return &Ladder{layers: layers}
}

// DefaultLadder returns the three required representations with the given
// consumer delay on the middle layer.
func DefaultLadder(slow time.Duration) *Ladder {
	specs := make([]LayerOptions, 0, len(LadderNames))
	for i, name := range LadderNames {
		spec := LayerOptions{Name: name, Buffer: BufferBytes}
		if i == 1 {
			spec.Slow = slow
		}
		specs = append(specs, spec)
	}
	return NewLadder(specs)
}

// Layers returns the layers in ladder order.
func (l *Ladder) Layers() []*Layer { return l.layers }

// Names returns layer names in ladder order.
func (l *Ladder) Names() []string {
	names := make([]string, len(l.layers))
	for i, layer := range l.layers {
		names[i] = layer.Name()
	}
	return names
}

// Fanout publishes one block to every live layer.
//
// A canceled layer is skipped rather than failing the publish: cancellation is
// per-layer state, and the other representations must not be disturbed by it.
func (l *Ladder) Fanout(ctx context.Context, b media.Block) error {
	var firstErr error
	for _, layer := range l.layers {
		if err := layer.Feed(ctx, b); err != nil {
			if merr.CodeOf(err) == merr.CodeLayerCanceled {
				continue
			}
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// Close ends every layer.
func (l *Ladder) Close() {
	for _, layer := range l.layers {
		layer.Close()
	}
}

// Stats returns per-layer snapshots in ladder order.
func (l *Ladder) Stats() []LayerStats {
	out := make([]LayerStats, len(l.layers))
	for i, layer := range l.layers {
		out[i] = layer.Stats()
	}
	return out
}

// Aggregate summarises the ladder for the report.
type Aggregate struct {
	Layers         []LayerStats
	Published      int
	BackpressureNs int64
}

// Aggregate returns the ladder summary.
func (l *Ladder) Aggregate() Aggregate {
	agg := Aggregate{Layers: l.Stats()}
	for _, s := range agg.Layers {
		if s.Offered > agg.Published {
			agg.Published = s.Offered
		}
		agg.BackpressureNs += s.BackpressureNs
	}
	return agg
}
