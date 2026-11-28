// Package verifier wires the container, stream, clock and report subsystems
// together into the per-input validation used by the command line. It owns
// the cross-subsystem rules: shared-timeline alignment, drift calibration and
// the mapping from every domain fault onto the deterministic report.
package verifier

import (
	"context"
	"math/big"

	"mediasync/internal/clock"
	"mediasync/internal/container"
	"mediasync/internal/fault"
	"mediasync/internal/report"
	"mediasync/internal/stream"
)

// DefaultDriftThresholdPPM is the largest drift that will be transparently
// calibrated instead of reported as a validation failure (5%).
const DefaultDriftThresholdPPM = 50_000

// Config controls one verification run.
type Config struct {
	Workers           int
	Queue             int
	DriftThresholdPPM int64
}

// Outcome is the fully evaluated result for one input pair.
type Outcome struct {
	Metrics report.Metrics
	Faults  []*fault.Fault
	Report  *report.Report
}

// VerifyPair validates one MP4 file against its framed packet stream. All
// faults, including subsystem errors, are collected and attributed; the
// returned error is non-nil only for operational (non-validation) failures.
func VerifyPair(ctx context.Context, mp4Path, packetsPath string) (*Outcome, error) {
	return VerifyPairWith(ctx, mp4Path, packetsPath, DefaultDriftThresholdPPM)
}

// VerifyPairWith is VerifyPair with an explicit drift threshold.
func VerifyPairWith(ctx context.Context, mp4Path, packetsPath string, driftPPM int64) (*Outcome, error) {
	out := &Outcome{}

	// --- Container -------------------------------------------------------
	file, err := container.Parse(ctx, mp4Path)
	if err != nil {
		if f, ok := fault.AsFault(err); ok {
			out.Faults = append(out.Faults, f)
			return out.finalize()
		}
		return nil, err // operational error (e.g. unreadable path)
	}

	dialogue := findTrack(file, "dialogue")
	if dialogue == nil {
		out.Faults = append(out.Faults, fault.New(fault.ErrContainerDecl, "", -1,
			"no dialogue track found; shared timeline cannot be established"))
		return out.finalize()
	}

	// --- Packet stream ---------------------------------------------------
	records, err := stream.ReadAll(ctx, packetsPath)
	if err != nil {
		if f, ok := fault.AsFault(err); ok {
			out.Faults = append(out.Faults, f)
			return out.finalize()
		}
		return nil, err
	}
	packets, xorRecords, err := stream.ParseRTP(records)
	if err != nil {
		if f, ok := fault.AsFault(err); ok {
			out.Faults = append(out.Faults, f)
			return out.finalize()
		}
		return nil, err
	}
	seq, err := stream.Analyze(packets)
	if err != nil {
		if f, ok := fault.AsFault(err); ok {
			out.Faults = append(out.Faults, f)
			return out.finalize()
		}
		return nil, err
	}

	frames, err := stream.ParseXOR(xorRecords)
	if err != nil {
		if f, ok := fault.AsFault(err); ok {
			out.Faults = append(out.Faults, f)
			return out.finalize()
		}
		return nil, err
	}
	recovery, err := stream.Recover(seq, frames)
	if err != nil {
		if f, ok := fault.AsFault(err); ok {
			out.Faults = append(out.Faults, f)
			return out.finalize()
		}
		return nil, err
	}
	out.Metrics.LossTotal = seq.MissingCount()
	out.Metrics.LossRecovered = recovery.Recovered
	for _, missing := range recovery.UnrecoveredSeqs {
		out.Faults = append(out.Faults, fault.New(fault.ErrPacketUnrecovered, "stream", -1,
			"sequence %d could not be recovered", missing))
	}

	// --- Shared timeline, drift and sync error ---------------------------
	evaluateTimeline(dialogue, seq, driftPPM, out)

	return out.finalize()
}

func findTrack(file *container.File, name string) *container.Track {
	for _, t := range file.Tracks {
		if t.Name == name {
			return t
		}
	}
	return nil
}

// evaluateTimeline compares the packet clock against the dialogue track on a
// shared seconds axis, detects and calibrates linear drift, and records both
// the raw and corrected maximum sync errors.
func evaluateTimeline(dialogue *container.Track, seq *stream.SequenceReport, driftPPM int64, out *Outcome) {
	tb, err := clock.NewTimebase(rtpClockRate)
	if err != nil {
		out.Faults = append(out.Faults, fault.New(fault.ErrIllegalTimebase, "stream", -1, "%v", err))
		return
	}

	// Build observed packet times in sequence order from extended stamps.
	packetTimes := make([]*big.Rat, 0, len(seq.Ordered))
	for _, p := range seq.Ordered {
		ext, ok := seq.ExtendedTS[p.Sequence]
		if !ok {
			continue // unrecovered hole: no time to compare
		}
		packetTimes = append(packetTimes, tb.TicksToSeconds(ext))
	}
	if len(packetTimes) < 2 {
		out.Faults = append(out.Faults, fault.New(fault.ErrSampleRange, "stream", -1,
			"not enough received packets to evaluate the timeline"))
		return
	}

	// Media reference times on the shared grid, and observed packet times.
	mediaTimes := mediaReferenceTimes(dialogue, len(packetTimes))

	// RTP timestamps have an arbitrary origin: align both series at their
	// first event so the "raw" error measures drift shape only, not the
	// meaningless clock-origin constant.
	mediaOrigin := mediaTimes[0]
	packetOrigin := packetTimes[0]
	relMedia := make([]*big.Rat, len(mediaTimes))
	relPacket := make([]*big.Rat, len(packetTimes))
	for i := range mediaTimes {
		relMedia[i] = new(big.Rat).Sub(mediaTimes[i], mediaOrigin)
		relPacket[i] = new(big.Rat).Sub(packetTimes[i], packetOrigin)
	}
	rawMax, _ := clock.MaxAbsDiffMillis(relMedia, relPacket)
	out.Metrics.SyncErrorMS = rawMax

	// Drift is measured between packet ordinal (the media grid) and the
	// observed RTP clock. Anchors at the first and last received packets.
	drift, derr := measureDrift(dialogue, tb, seq, driftPPM)
	if derr != nil {
		if f, ok := fault.AsFault(derr); ok {
			out.Faults = append(out.Faults, f)
		}
		return
	}
	if drift == nil {
		return // no measurable drift window
	}
	out.Metrics.DriftPPM = drift.PPM

	// Calibrate every observed packet time onto the media grid.
	calibrated := make([]*big.Rat, 0, len(packetTimes))
	for _, p := range seq.Ordered {
		ext, ok := seq.ExtendedTS[p.Sequence]
		if !ok {
			continue
		}
		calibrated = append(calibrated, drift.Calibrate(tb, ext))
	}
	calMax, _ := clock.MaxAbsDiffMillis(mediaTimes, calibrated)
	out.Metrics.SyncErrorMS = calMax
	out.Metrics.RawSyncErrorMS = rawMax
}

// mediaReferenceTimes returns the dialogue sample start times for the first n
// comparable events, expressed in seconds on the shared timeline.
func mediaReferenceTimes(dialogue *container.Track, n int) []*big.Rat {
	if n > len(dialogue.Marks) {
		n = len(dialogue.Marks)
	}
	out := make([]*big.Rat, n)
	for i := 0; i < n; i++ {
		out[i] = dialogue.Marks[i].CalibSeconds
	}
	return out
}

func (o *Outcome) finalize() (*Outcome, error) {
	rep, err := report.Build(o.Metrics, o.Faults)
	if err != nil {
		return nil, err
	}
	o.Report = rep
	return o, nil
}
