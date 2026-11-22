// Package report turns session results into deterministic, re-computable
// JSON statistics. Field order is fixed via explicit structs, and slice
// contents are ordered so repeated runs produce byte-identical output.
package report

import (
	"encoding/json"
)

// GapJSON is the JSON representation of one sequence gap.
type GapJSON struct {
	SSRC         uint32 `json:"ssrc"`
	FirstMissing uint16 `json:"first_missing"`
	MissingCount int    `json:"missing_count"`
	AfterPrev    uint16 `json:"after_prev"`
	BeforeNext   uint16 `json:"before_next"`
}

// FrameJSON is the JSON representation of one reassembled frame.
type FrameJSON struct {
	SSRC         uint32 `json:"ssrc"`
	Timestamp    uint32 `json:"timestamp"`
	Complete     bool   `json:"complete"`
	GapCount     int    `json:"gap_count"`
	PacketCount  int    `json:"packet_count"`
	PayloadHex   string `json:"payload_hex"`
}

// TransitionJSON is one observed legal state transition.
type TransitionJSON struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Method string `json:"method"`
}

// TransportReport covers one transport mode.
type TransportReport struct {
	Transport         string           `json:"transport"`
	OK                bool             `json:"ok"`
	Failure           string           `json:"failure,omitempty"`
	FailureStage      string           `json:"failure_stage,omitempty"`
	FailureMethod     string           `json:"failure_method,omitempty"`
	StateTransitions  []TransitionJSON `json:"state_transitions"`
	StateTransitionN  int              `json:"state_transition_count"`
	SequenceGaps      []GapJSON        `json:"sequence_gaps"`
	SequenceGapCount  int              `json:"sequence_gap_count"`
	FramesComplete    int              `json:"frames_complete"`
	FramesIncomplete  int              `json:"frames_incomplete"`
	Frames            []FrameJSON      `json:"frames"`
	PacketsReceived   int              `json:"packets_received"`
	LatePackets       int              `json:"late_packets"`
}

// Report is the top-level document.
type Report struct {
	Scenario        string            `json:"scenario"`
	ExpectationsMet bool              `json:"expectations_met"`
	ExpectationErrs []string          `json:"expectation_errors"`
	Transports      []TransportReport `json:"transports"`
}

// Expectation captures the scenario's predicted counts.
type Expectation struct {
	Transitions    int
	SequenceGaps   int
	FramesComplete int
	FramesIncomplete int
}

// Options controls report building.
type Options struct {
	Scenario    string
	Expectation Expectation
}

// Builder accumulates per-transport reports.
type Builder struct {
	opts Options
	rep  Report
}

// NewBuilder creates a report builder.
func NewBuilder(opts Options) *Builder {
	return &Builder{
		opts: opts,
		rep: Report{
			Scenario:        opts.Scenario,
			ExpectationsMet: true,
		},
	}
}

// AddResult converts one transport.Result into JSON-friendly form,
// validates it against the scenario expectations and appends it.
func (b *Builder) AddResult(transportName string, result ResultView) {
	tr := TransportReport{
		Transport:        transportName,
		OK:               result.Failure() == nil,
		StateTransitions: make([]TransitionJSON, 0),
		SequenceGaps:     make([]GapJSON, 0),
		Frames:           make([]FrameJSON, 0),
	}

	if err := result.Failure(); err != nil {
		tr.Failure = err.Error()
		tr.FailureStage = string(result.FailureStage())
		tr.FailureMethod = string(result.FailureMethod())
	}

	for _, t := range result.Transitions() {
		tr.StateTransitions = append(tr.StateTransitions, TransitionJSON{
			From:   string(t.From),
			To:     string(t.To),
			Method: string(t.Method),
		})
	}
	tr.StateTransitionN = len(tr.StateTransitions)

	for _, g := range result.Gaps() {
		tr.SequenceGaps = append(tr.SequenceGaps, GapJSON{
			SSRC:         g.SSRC,
			FirstMissing: g.FirstMissing,
			MissingCount: g.MissingCount,
			AfterPrev:    g.AfterPrev,
			BeforeNext:   g.BeforeNext,
		})
	}
	tr.SequenceGapCount = result.GapCount()

	tr.FramesComplete, tr.FramesIncomplete = result.FrameStats()
	for _, f := range result.Frames() {
		tr.Frames = append(tr.Frames, FrameJSON{
			SSRC:        f.SSRC,
			Timestamp:   f.Timestamp,
			Complete:    f.Complete,
			GapCount:    f.GapCount,
			PacketCount: f.PacketCount,
			PayloadHex:  encodeHex(f.Payload),
		})
	}
	tr.PacketsReceived = result.PacketsSeen()
	tr.LatePackets = result.LatePackets()

	b.validate(transportName, &tr)
	b.rep.Transports = append(b.rep.Transports, tr)
}

func (b *Builder) validate(name string, tr *TransportReport) {
	e := b.opts.Expectation
	add := func(format string, args ...any) {
		b.rep.ExpectationsMet = false
		b.rep.ExpectationErrs = append(b.rep.ExpectationErrs,
			sprintf(name+": "+format, args...))
	}
	if !tr.OK {
		add("session failed at %s/%s: %s", tr.FailureStage, tr.FailureMethod, tr.Failure)
	}
	if tr.StateTransitionN != e.Transitions {
		add("state_transitions=%d, want %d", tr.StateTransitionN, e.Transitions)
	}
	if tr.SequenceGapCount != e.SequenceGaps {
		add("sequence_gaps=%d, want %d", tr.SequenceGapCount, e.SequenceGaps)
	}
	if tr.FramesComplete != e.FramesComplete {
		add("frames_complete=%d, want %d", tr.FramesComplete, e.FramesComplete)
	}
	if tr.FramesIncomplete != e.FramesIncomplete {
		add("frames_incomplete=%d, want %d", tr.FramesIncomplete, e.FramesIncomplete)
	}
}

// Build returns the finalized report.
func (b *Builder) Build() *Report {
	if b.rep.ExpectationErrs == nil {
		b.rep.ExpectationErrs = []string{}
	}
	return &b.rep
}

// Marshal renders the report as indented JSON with a trailing newline.
func (r *Report) Marshal() ([]byte, error) {
	out, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}
