// Package pipeline wires the stages together: EBML read, timestamp repair,
// index rebuild, fan-out, per-layer RTP recovery, fan-in and reporting.
//
// Each stage is also usable on its own; this package is the single
// composition the CLI runs. Keeping the wiring in one place is what makes the
// cross-stage invariants auditable, and lets tests assert that an error
// raised at the parse stage actually reaches the index or the switch check.
package pipeline

import (
	"bytes"
	"context"
	"io"

	"mediad/internal/ebml"
	"mediad/internal/fanin"
	"mediad/internal/fanout"
	"mediad/internal/merr"
	"mediad/internal/mp4x"
	"mediad/internal/repair"
	"mediad/internal/report"
)

// Run executes the whole pipeline over an in-memory Matroska/WebM document.
func Run(ctx context.Context, data []byte, opts Options) (*Output, error) {
	opts = opts.withDefaults()

	// 1. Stream the document. Confirmed blocks are appended as they arrive,
	// so a damaged tail still leaves a usable prefix.
	walker := ebml.NewWalker(bytes.NewReader(data), ebml.ReaderOptions{})
	demux := ebml.NewDemuxer(walker)
	var raw []ebml.Block
	demux.OnBlock = func(b ebml.Block) { raw = append(raw, b) }
	_, readErr := demux.Run()
	info := demux.Info()
	var damage error
	if readErr != nil && readErr != io.EOF {
		damage = readErr
	}

	// 2. Repair timestamps onto the shared clock.
	rep, err := repair.Repair(raw, info.Tracks, opts.Repair)
	if err != nil {
		return nil, err
	}

	// 3. Rebuild the random-access index, keeping a confirmed prefix.
	idx, idxErr := repair.BuildIndexFromRepair(rep, damage, opts.Index)
	if idxErr != nil {
		if !merr.IsDamage(merr.CodeOf(idxErr)) {
			return nil, idxErr
		}
		if damage == nil {
			damage = idxErr
		}
	}

	out := &Output{
		Repair:   rep,
		Index:    idx,
		FileInfo: info,
		Damage:   damage,
	}

	// 4. Cross-check the index against an independently parsed MP4 index.
	if len(idx.Index.Entries) > 0 {
		cc, ccErr := mp4x.Verify(idx.Index.Entries)
		if ccErr != nil {
			if damage == nil {
				damage = ccErr
			}
		} else {
			out.CrossCheck = cc
		}
	}

	// 5. Fan out to the representations, measuring publish latency.
	ladder, latencies, err := runLadder(ctx, rep.Blocks, opts)
	if err != nil {
		return nil, err
	}
	out.Ladder = ladder
	out.LadderStats = ladder.Layers

	// 6. Recover every representation through its own RTP path. A layer the
	// caller cancelled is truncated to its confirmed prefix here, which is
	// exactly what a consumer that walked away would leave behind.
	streams, wire, absent := recoverStreams(ctx, rep.Blocks, opts)
	out.RTP = wire

	// 7. Fan in and cross-check representation switches.
	order := append([]string(nil), fanout.LadderNames[:]...)
	plan := buildPlan(rep.Blocks, order, opts.SwitchFraction, absent)
	merge, err := fanin.Merge(streams, order, fanin.Options{Plan: plan})
	if err != nil {
		return nil, err
	}
	out.Merge = merge

	consistent := true
	for _, l := range ladder.Layers {
		if l.PeakBytes > fanout.BufferBytes {
			consistent = false
		}
	}
	// A cancelled layer must be the only one affected: the others must have
	// delivered every block.
	if opts.CancelLayer != "" {
		for _, l := range ladder.Layers {
			if l.Name == opts.CancelLayer {
				continue
			}
			if l.Offered != len(rep.Blocks) {
				consistent = false
			}
		}
	}

	out.Summary = report.WithHash(report.Build(report.Inputs{
		Repair:           rep,
		Index:            idx,
		Ladder:           ladder,
		Merge:            merge,
		RTP:              wire,
		LatenciesNs:      latencies,
		Damage:           damage,
		MP4Entries:       out.CrossCheck.Entries,
		LayersConsistent: consistent,
	}))
	return out, nil
}
