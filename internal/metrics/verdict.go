// Package metrics accounts for every packet and classifies continuity
// counter behaviour per PID.
package metrics

// Verdict classifies one packet for downstream consumers.
type Verdict int

const (
	// VerdictOK means the packet continues the expected sequence.
	VerdictOK Verdict = iota
	// VerdictFirst means this is the first packet seen for the PID.
	VerdictFirst
	// VerdictDuplicate means the counter repeated, so the payload is a copy.
	VerdictDuplicate
	// VerdictJump means one or more packets were lost or reordered.
	VerdictJump
	// VerdictTransportError means the transport_error_indicator was set.
	VerdictTransportError
	// VerdictAFOnly means an adaptation-field-only packet (no payload).
	VerdictAFOnly
	// VerdictDiscontinuity means the adaptation field announced a discontinuity.
	VerdictDiscontinuity
	// VerdictUnjudgeable means the packet follows damaged input and cannot be
	// checked against a previous counter.
	VerdictUnjudgeable
)

func (v Verdict) String() string {
	switch v {
	case VerdictOK:
		return "ok"
	case VerdictFirst:
		return "first"
	case VerdictDuplicate:
		return "duplicate"
	case VerdictJump:
		return "jump"
	case VerdictTransportError:
		return "transport_error"
	case VerdictAFOnly:
		return "af_only"
	case VerdictDiscontinuity:
		return "discontinuity"
	case VerdictUnjudgeable:
		return "unjudgeable"
	}
	return "unknown"
}

// ReliablePayload reports whether the packet payload may be parsed. A
// duplicate is usable only as a byte copy, never as fresh PSI data.
func (v Verdict) ReliablePayload() bool {
	return v == VerdictOK || v == VerdictFirst
}

// PIDStats is the continuity summary of one PID.
