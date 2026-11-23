// Package testkit builds deterministic fixtures and fault injections.
//
// Everything is generated in memory: no external files, no network. Each
// builder is a pure function of its seed, so the demo and the verifier can
// reproduce byte-identical input across runs and across machines.
package testkit

import (
	"fmt"
)

// Fault names accepted by the CLI.
const (
	FaultNone            = "none"
	FaultClusterTruncate = "cluster-truncate"
	FaultTimestampWrap   = "timestamp-wrap"
	FaultUnknownSizeNest = "unknown-size-nest"
	FaultRTPLoss         = "rtp-loss"
	FaultSlowConsumer    = "slow-consumer-cancel"
)

// KnownFaults lists every injectable fault, in a stable order.
var KnownFaults = []string{
	FaultClusterTruncate,
	FaultTimestampWrap,
	FaultUnknownSizeNest,
	FaultRTPLoss,
	FaultSlowConsumer,
}

// FaultResult is a document plus any non-structural defects it carries.
type FaultResult struct {
	Data []byte
	Spec DocSpec
	// Fault is the injected fault name.
	Fault string
	// LossEvery and SlowConsumerMs are transport-side faults that are applied
	// by the pipeline rather than baked into the bytes.
	LossEvery      int
	SlowConsumerMs int
	// CancelLayer names a representation whose consumer walks away.
	CancelLayer string
	// CutBytes is how many bytes were removed for a truncation fault.
	CutBytes int
}

// BuildFaulted produces the document for the given fault.
func BuildFaulted(fault string, seed int64) (*FaultResult, error) {
	spec := DemoDocSpec(seed)
	res := &FaultResult{Spec: spec, Fault: fault}

	switch fault {
	case FaultNone, "":
		res.Fault = FaultNone
	case FaultClusterTruncate:
		// Build a document whose last cluster is long, then cut mid-cluster.
		doc, err := BuildDoc(spec)
		if err != nil {
			return nil, err
		}
		cut := len(doc.Data) / 5
		if cut < 4096 {
			cut = 4096
		}
		if cut >= len(doc.Data) {
			return nil, fmt.Errorf("testkit: document too small to truncate")
		}
		res.Data = append([]byte(nil), doc.Data[:len(doc.Data)-cut]...)
		res.CutBytes = cut
		return res, nil

	case FaultTimestampWrap:
		// Start the counter just before the 32-bit wrap so several blocks
		// straddle it.
		// The generator adds a start offset as uint32, so the counter begins
		// at 2^32-400 ms and wraps a few blocks into the clip.
		spec.Tracks[0].StartOffsetMs = ^uint32(0) - 399
		spec.Tracks[1].StartOffsetMs = ^uint32(0) - 399
	case FaultUnknownSizeNest:
		// Unknown-size Segment holding unknown-size Clusters is the default
		// shape, so this fault counter-points: make it nested twice as deep
		// by leaving BlockGroups unknown-size as well.
		spec.SegmentUnknown = true
		spec.ClusterUnknown = true
	case FaultRTPLoss:
		res.LossEvery = 17
	case FaultSlowConsumer:
		res.SlowConsumerMs = 120
		res.CancelLayer = "1080p"

	default:
		return nil, fmt.Errorf("testkit: unknown fault %q", fault)
	}

	doc, err := BuildDoc(spec)
	if err != nil {
		return nil, err
	}
	res.Data = doc.Data
	return res, nil
}
