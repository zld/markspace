package report

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	rtpx "rtspcheck/internal/rtp"
	"rtspcheck/internal/session"
)

type fakeView struct {
	err         error
	stage       session.State
	method      session.Method
	transitions []session.Transition
	gaps        []rtpx.Gap
	frames      []rtpx.Frame
	seen        int
	late        int
}

func (f *fakeView) Failure() error                       { return f.err }
func (f *fakeView) FailureStage() session.State          { return f.stage }
func (f *fakeView) FailureMethod() session.Method        { return f.method }
func (f *fakeView) Transitions() []session.Transition    { return f.transitions }
func (f *fakeView) Gaps() []rtpx.Gap                     { return f.gaps }
func (f *fakeView) GapCount() int {
	n := 0
	for _, g := range f.gaps {
		n += g.MissingCount
	}
	return n
}
func (f *fakeView) Frames() []rtpx.Frame { return f.frames }
func (f *fakeView) FrameStats() (int, int) {
	complete, incomplete := 0, 0
	for _, fr := range f.frames {
		if fr.Complete {
			complete++
		} else {
			incomplete++
		}
	}
	return complete, incomplete
}
func (f *fakeView) PacketsSeen() int { return f.seen }
func (f *fakeView) LatePackets() int { return f.late }

func happyView() *fakeView {
	return &fakeView{
		transitions: []session.Transition{
			{From: session.StateInit, To: session.StateOptions, Method: session.MethodOptions},
			{From: session.StateOptions, To: session.StateDescribed, Method: session.MethodDescribe},
			{From: session.StateDescribed, To: session.StateSetup, Method: session.MethodSetup},
			{From: session.StateSetup, To: session.StatePlaying, Method: session.MethodPlay},
			{From: session.StatePlaying, To: session.StateTeardown, Method: session.MethodTeardown},
			{From: session.StateTeardown, To: session.StateClosed, Method: session.MethodTeardown},
		},
		gaps: []rtpx.Gap{{SSRC: 1, FirstMissing: 4, MissingCount: 1, AfterPrev: 3, BeforeNext: 5}},
		frames: []rtpx.Frame{
			{SSRC: 1, Timestamp: 1000, Complete: true, PacketCount: 4, Payload: []byte{0xaa}},
			{SSRC: 1, Timestamp: 3000, Complete: false, GapCount: 1, PacketCount: 1},
			{SSRC: 1, Timestamp: 4000, Complete: true, PacketCount: 2, Payload: []byte{0xd0, 0xd1}},
		},
		seen: 9,
	}
}

func TestReportHappyPathMatchesExpectations(t *testing.T) {
	b := NewBuilder(Options{
		Scenario: "generated",
		Expectation: Expectation{
			Transitions: 6, SequenceGaps: 1,
			FramesComplete: 2, FramesIncomplete: 1,
		},
	})
	b.AddResult("udp", happyView())
	b.AddResult("interleaved", happyView())

	rep := b.Build()
	if !rep.ExpectationsMet || len(rep.ExpectationErrs) != 0 {
		t.Fatalf("expectations should be met, got %v", rep.ExpectationErrs)
	}
	if len(rep.Transports) != 2 {
		t.Fatalf("transport reports = %d, want 2", len(rep.Transports))
	}
	for _, tr := range rep.Transports {
		if tr.Transport != "udp" && tr.Transport != "interleaved" {
			t.Fatalf("unexpected transport %q", tr.Transport)
		}
		if tr.StateTransitionN != 6 || tr.SequenceGapCount != 1 ||
			tr.FramesComplete != 2 || tr.FramesIncomplete != 1 || !tr.OK {
			t.Fatalf("wrong counts: %+v", tr)
		}
	}

	raw, err := rep.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	for _, key := range []string{"transport", "state_transitions", "sequence_gaps",
		"frames_complete", "frames_incomplete"} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("JSON missing required key %q", key)
		}
	}
}

func TestReportDeterministicOutput(t *testing.T) {
	build := func() []byte {
		b := NewBuilder(Options{
			Scenario: "generated",
			Expectation: Expectation{
				Transitions: 6, SequenceGaps: 1,
				FramesComplete: 2, FramesIncomplete: 1,
			},
		})
		b.AddResult("udp", happyView())
		raw, err := b.Build().Marshal()
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	if string(build()) != string(build()) {
		t.Fatal("report output is not deterministic across builds")
	}
}

func TestReportFailureAndExpectationMismatch(t *testing.T) {
	v := happyView()
	v.err = errors.New("connection reset")
	v.stage = session.StatePlaying
	v.method = session.MethodTeardown
	v.transitions = v.transitions[:4] // session died before TEARDOWN
	v.frames = v.frames[:1]

	b := NewBuilder(Options{
		Scenario: "generated",
		Expectation: Expectation{
			Transitions: 6, SequenceGaps: 1,
			FramesComplete: 2, FramesIncomplete: 1,
		},
	})
	b.AddResult("udp", v)
	rep := b.Build()

	if rep.ExpectationsMet {
		t.Fatal("expectations should not be met")
	}
	joined := strings.Join(rep.ExpectationErrs, "\n")
	for _, want := range []string{"session failed", "state_transitions=4",
		"frames_complete=1", "frames_incomplete=0"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expectation errors %q missing %q", joined, want)
		}
	}
	tr := rep.Transports[0]
	if tr.OK || tr.Failure != "connection reset" ||
		tr.FailureStage != "Playing" || tr.FailureMethod != "TEARDOWN" {
		t.Fatalf("failure fields wrong: %+v", tr)
	}
}
