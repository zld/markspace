// Package segment fans recovered layers into one shared clock and
// cross-checks their boundaries. A layer whose start, end or frame boundary
// drifts beyond tolerance is isolated; every other layer keeps producing.
package segment

import (
	"fmt"

	"archive/internal/clock"
	"archive/internal/rtp"
)

// MaxBoundaryErrorNS is the accepted deviation between a layer's confirmed
// frame boundary and the shared clock. Boundary cases must resolve exactly.
const MaxBoundaryErrorNS int64 = 0

// LayerInput is one recovered layer presented for fan-in.
type LayerInput struct {
	Layer rtp.Layer
	// Result is the recovered transport state.
	Result *rtp.Result
	// StartNS and EndNS are the layer's confirmed media span on the shared
	// clock, derived from its own timestamps.
	StartNS         int64
	EndNS           int64
	FrameIntervalNS int64
	// ExpectedStartNS and ExpectedEndNS come from the timeline. They are what
	// every layer must agree on.
	ExpectedStartNS int64
	ExpectedEndNS   int64
}

// LayerStatus is the fan-in verdict for one layer.
type LayerStatus struct {
	Name             string
	StartNS          int64
	EndNS            int64
	StartErrorNS     int64
	EndErrorNS       int64
	MaxBoundaryErrNS int64
	Frames           int
	Gaps             int
	Lost             int
	Duplicates       int
	Reordered        int
	Isolated         bool
	Reason           string
}

// Summary aggregates fan-in output.
type Summary struct {
	Layers          []LayerStatus
	Active          int
	Isolated        int
	ManifestAlignNS int64
	SharedStartNS   int64
	SharedEndNS     int64
	FrameCount      int64
	Reason          string
}

// FanIn validates every layer against the shared clock. If the shared span is
// unknown (zero), it is taken from the layer with the smallest start error.
// A layer is isolated when any of these exceed tolerance:
//   - start deviation, end deviation, or any frame boundary offset.
//
// Layers that are isolated are named in the summary; survivors still produce.
func FanIn(inputs []LayerInput, toleranceNS int64) (*Summary, error) {
	if len(inputs) == 0 {
		return nil, clock.New(clock.KindInvalidMetadata, "no layers to fan in")
	}
	if toleranceNS < 0 {
		return nil, clock.Newf(clock.KindOutOfRange, "negative tolerance %d ns", toleranceNS)
	}
	sum := &Summary{}
	var maxErr int64
	for _, in := range inputs {
		st := evaluate(in, toleranceNS)
		if !st.Isolated {
			// Alignment evidence covers surviving layers only. An isolated
			// layer's drift is already reported in its own reason, and must
			// not inflate the manifest alignment of a clean output.
			if st.MaxBoundaryErrNS > maxErr {
				maxErr = st.MaxBoundaryErrNS
			}
			sum.Active++
			if sum.Active == 1 || st.StartNS < sum.SharedStartNS {
				sum.SharedStartNS = st.StartNS
			}
			if st.EndNS > sum.SharedEndNS {
				sum.SharedEndNS = st.EndNS
			}
			sum.FrameCount += int64(st.Frames)
		} else {
			sum.Isolated++
		}
		sum.Layers = append(sum.Layers, st)
	}
	sum.ManifestAlignNS = maxErr
	if sum.Active == 0 {
		sum.Reason = "every layer exceeded boundary tolerance"
		return nil, clock.New(clock.KindMisaligned, sum.Reason)
	}
	return sum, nil
}

func evaluate(in LayerInput, tol int64) LayerStatus {
	st := LayerStatus{Name: in.Layer.Name}

	startErr := absDiff(in.StartNS, in.ExpectedStartNS)
	endErr := absDiff(in.EndNS, in.ExpectedEndNS)
	st.StartErrorNS = startErr
	st.EndErrorNS = endErr
	st.StartNS = in.StartNS
	st.EndNS = in.EndNS

	if in.Result != nil {
		st.Duplicates = in.Result.Duplicates
		st.Reordered = in.Result.Reordered
		st.Gaps = len(in.Result.Gaps)
		st.Lost = in.Result.Lost
	}

	if in.FrameIntervalNS <= 0 {
		st.Isolated = true
		st.Reason = "frame interval is not positive"
		return st
	}
	if in.EndNS < in.StartNS {
		st.Isolated = true
		st.Reason = "layer end precedes start"
		return st
	}
	frames := int((in.EndNS - in.StartNS) / in.FrameIntervalNS)
	spanErr := absDiff(in.EndNS-in.StartNS, in.ExpectedEndNS-in.ExpectedStartNS)
	if spanErr > tol {
		st.Isolated = true
		st.Reason = fmt.Sprintf("span %d ns deviates from expected %d ns by %d ns",
			in.EndNS-in.StartNS, in.ExpectedEndNS-in.ExpectedStartNS, spanErr)
		return st
	}
	// Confirm every frame boundary sits on the shared grid.
	var worst int64
	for f := 0; f <= frames; f++ {
		ts := in.StartNS + int64(f)*in.FrameIntervalNS
		want := in.ExpectedStartNS + int64(f)*in.FrameIntervalNS
		if d := absDiff(ts, want); d > worst {
			worst = d
		}
	}
	st.MaxBoundaryErrNS = worst
	if worst > tol {
		st.Isolated = true
		st.Reason = fmt.Sprintf("frame boundary deviates by %d ns (tolerance %d ns)", worst, tol)
		return st
	}
	if startErr > tol {
		st.Isolated = true
		st.Reason = fmt.Sprintf("start deviates by %d ns", startErr)
		return st
	}
	if endErr > tol {
		st.Isolated = true
		st.Reason = fmt.Sprintf("end deviates by %d ns", endErr)
		return st
	}
	if in.Result != nil && !recoverable(in.Result) {
		st.Isolated = true
		st.Reason = "transport recovery produced no usable packets"
		return st
	}
	st.Frames = frames
	return st
}

func recoverable(r *rtp.Result) bool {
	return len(r.Ordered) > 0
}

func absDiff(a, b int64) int64 {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d
}
