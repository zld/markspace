package clock

import (
	"example.com/mediatimeline/internal/model"
)

// DecisionKind classifies one repair action taken while decoding timestamps.
type DecisionKind string

// Repair decision kinds.
const (
	DecisionWrap      DecisionKind = "wrap-extended"
	DecisionReorder   DecisionKind = "reorder-clamped"
	DecisionOrigin    DecisionKind = "origin-shifted"
	DecisionBackward  DecisionKind = "backward-rejected"
	DecisionMonotonic DecisionKind = "monotonic-clamped"
	DecisionFirstSeen DecisionKind = "first-seen"
)

// Decision records a single repair or rejection, preserving the raw tick so
// the demo output and tests can prove the repair was not silent.
type Decision struct {
	Track       model.TrackID `json:"track"`
	Kind        DecisionKind  `json:"kind"`
	RawTicks    int64         `json:"raw_ticks"`
	Repaired    int64         `json:"repaired_ticks"`
	Epoch       int64         `json:"epoch"`
	Recoverable bool          `json:"recoverable"`
	Message     string        `json:"message"`
}
