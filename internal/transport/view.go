package transport

import (
	rtpx "rtspcheck/internal/rtp"
	"rtspcheck/internal/session"
)

// The following methods make *Result satisfy report.ResultView without
// the report package importing the transport package.

// Failure returns the session error, or nil when the session succeeded.
func (r *Result) Failure() error { return r.Err }

// FailureStage returns the state the machine was in when it failed.
func (r *Result) FailureStage() session.State { return r.Stage }

// FailureMethod returns the method whose event caused the failure.
func (r *Result) FailureMethod() session.Method { return r.Method }

// Transitions returns the observed legal state transitions.
func (r *Result) Transitions() []session.Transition {
	return r.Machine.Transitions()
}

// Gaps returns the RTP sequence gaps recorded during the session.
func (r *Result) Gaps() []rtpx.Gap { return r.Assembler.Gaps() }

// GapCount returns the total number of missing sequence numbers.
func (r *Result) GapCount() int { return r.Assembler.GapCount() }

// Frames returns every reassembled frame.
func (r *Result) Frames() []rtpx.Frame { return r.Assembler.Frames() }

// FrameStats returns complete and incomplete frame counts.
func (r *Result) FrameStats() (int, int) { return r.Assembler.Stats() }

// PacketsSeen returns the number of RTP packets delivered to the
// assembler, including packets of frames later found incomplete.
func (r *Result) PacketsSeen() int { return r.packetsSeen }

// LatePackets returns how many packets were dropped because their
// frame had already been finalized.
func (r *Result) LatePackets() int { return r.Assembler.LatePackets() }
