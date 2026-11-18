package rtsp

import (
	"errors"
	"testing"
)

func TestSessionHappyPath(t *testing.T) {
	s := NewSession()
	methods := []Method{
		MethodOptions, MethodDescribe, MethodSetup, MethodSetup, MethodPlay,
		MethodTeardown,
	}
	for _, m := range methods {
		if err := s.Apply(m); err != nil {
			t.Fatalf("Apply(%s): %v", m, err)
		}
	}
	if s.State() != StateTeardown {
		t.Fatalf("final state = %s", s.State())
	}
	if got := s.TransitionCount(); got != len(methods) {
		t.Fatalf("transition count = %d want %d", got, len(methods))
	}
	if s.RejectedCount() != 0 {
		t.Fatalf("unexpected rejections: %d", s.RejectedCount())
	}
}

func TestSessionInvalidOrders(t *testing.T) {
	cases := []struct {
		name    string
		methods []Method
		failAt  int
	}{
		{"describe before options", []Method{MethodDescribe}, 0},
		{"setup before describe", []Method{MethodOptions, MethodSetup}, 1},
		{"play before setup", []Method{MethodOptions, MethodDescribe, MethodPlay}, 2},
		{"request after teardown",
			[]Method{
				MethodOptions, MethodDescribe, MethodSetup, MethodPlay,
				MethodTeardown, MethodPlay,
			}, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSession()
			var err error
			for i, m := range tc.methods {
				err = s.Apply(m)
				if i == tc.failAt {
					var inv *ErrInvalidTransition
					if !errors.As(err, &inv) {
						t.Fatalf("expected ErrInvalidTransition at %d, got %v", i, err)
					}
				} else if err != nil {
					t.Fatalf("unexpected error at %d: %v", i, err)
				}
			}
			// The rejected move must not be counted.
			if s.TransitionCount() != tc.failAt {
				t.Fatalf("transitions = %d want %d", s.TransitionCount(), tc.failAt)
			}
			if s.RejectedCount() != 1 {
				t.Fatalf("rejected = %d want 1", s.RejectedCount())
			}
		})
	}
}

func TestSessionTeardownFromSetup(t *testing.T) {
	s := NewSession()
	for _, m := range []Method{MethodOptions, MethodDescribe, MethodSetup, MethodTeardown} {
		if err := s.Apply(m); err != nil {
			t.Fatalf("Apply(%s): %v", m, err)
		}
	}
	if s.State() != StateTeardown || s.TransitionCount() != 4 {
		t.Fatalf("unexpected state/count: %s %d", s.State(), s.TransitionCount())
	}
}

func TestSessionReset(t *testing.T) {
	s := NewSession()
	_ = s.Apply(MethodOptions)
	_ = s.Apply(MethodDescribe)
	_ = s.Apply(MethodPlay) // illegal, rejected
	s.Reset()
	if s.State() != StateInit || s.TransitionCount() != 0 || s.RejectedCount() != 0 {
		t.Fatalf("reset residue: state=%s transitions=%d rejected=%d",
			s.State(), s.TransitionCount(), s.RejectedCount())
	}
	if tr := s.Transitions(); len(tr) != 0 {
		t.Fatalf("transition log not cleared: %v", tr)
	}
}

func TestStateAndMethodStrings(t *testing.T) {
	if StatePlay.String() != "PLAY" || MethodTeardown.String() != "TEARDOWN" {
		t.Fatal("string rendering wrong")
	}
}
