package pipeline

import (
	"mediad/internal/model"
	"mediad/internal/recovery"
)

// Chunk is one arrival of framed bytes on the reference clock.
type Chunk struct {
	ArrivalMs int64
	Data      []byte
	order     int
}

// Send is one transmission the simulated sender made. The receiver keeps
// a bounded retransmit cache populated from this log; a packet that the
// network drops is still recoverable from the sender side.
type Send struct {
	AtMs  int64
	Event model.Event
}

// Truth records ground truth the generator knows and the pipeline must
// estimate: the real A/V offset, the number of media packets lost, and
// the sender's transmission log.
type Truth struct {
	TrueOffsetMs int64
	SentMedia    int
	LostMedia    int
	Sent         []Send
	// LostKeys are the wire keys of packets the network dropped. Recovery
	// rate is measured against exactly this set.
	LostKeys []recovery.Key
}

// Spec describes one generated scenario.
type Spec struct {
	Name            string
	Loss            float64
	ReorderMs       int64
	FeedbackDelayMs int64
	LateNACKMs      int64 // if non-zero, every LateNACKEvery-th NACK uses it
	LateNACKEvery   int
	DupNACKEvery    int
	PhantomNACK     bool
	SendSR          bool
	// DriftPPM skews every Sender Report's wall clock by a ramp over the
	// stream, modelling a sender whose clock gains (positive) or loses
	// time against the reference clock. Zero means no drift.
	DriftPPM int64
	// LateSampleMs delays every LateSampleEvery-th media arrival by that
	// many milliseconds. A delay beyond the late window exercises the
	// drop path; a delay inside it exercises the replay path.
	LateSampleMs     int64
	LateSampleEvery  int
	WrapSeq          bool
	TruncateTail     bool
	CorruptExtension bool
	CorruptFraming   bool
	Duplicates       float64
	InjectAmbiguous  bool
}

const (
	durationMs   = 3000
	videoStepMs  = 33
	audioStepMs  = 20
	baseDelayMs  = 40
	videoSSRC    = 0x11111111
	audioSSRC    = 0x22222222
	trueOffsetMs = 25 // video presentation runs 25ms ahead of audio
)

// Fixtures returns the declared acceptance scenarios.
func Fixtures() []Spec {
	base := Spec{
		Name: "normal", Loss: 0.05, ReorderMs: 80, FeedbackDelayMs: 100,
		SendSR: true, Duplicates: 0.01, DupNACKEvery: 7, PhantomNACK: true,
	}
	wrap := base
	wrap.Name, wrap.WrapSeq, wrap.InjectAmbiguous = "wrap", true, true
	trunc := base
	trunc.Name, trunc.TruncateTail = "truncated", true
	corrupt := base
	corrupt.Name, corrupt.CorruptExtension, corrupt.CorruptFraming = "corrupt", true, true
	late := base
	late.Name, late.FeedbackDelayMs = "late-rtcp", 200
	late.LateNACKMs, late.LateNACKEvery = 1200, 4
	late.LateSampleMs, late.LateSampleEvery = 900, 9
	nosr := base
	nosr.Name, nosr.SendSR = "no-sr", false
	drift := base
	drift.Name, drift.DriftPPM = "clock-drift", 2000
	return []Spec{base, wrap, trunc, corrupt, late, nosr, drift}
}

// FixtureNames lists the acceptance scenario names in declaration order.
func FixtureNames() []string {
	specs := Fixtures()
	names := make([]string, len(specs))
	for i, s := range specs {
		names[i] = s.Name
	}
	return names
}
