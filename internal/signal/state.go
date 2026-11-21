// Package signal implements an in-process HTTP signaling rendezvous with a
// strict per-link negotiation state machine.
package signal

import (
	"errors"
	"sync"
	"time"
)

// Link names correlate the two WebRTC hops of one forwarding round.
const (
	LinkIngress = "ingress"
	LinkEgress  = "egress"
)

// Signaling errors. The HTTP layer maps each to a stable code and status.
var (
	ErrDuplicateRequest   = errors.New("duplicate signaling request")
	ErrExpiredRequest     = errors.New("expired signaling request")
	ErrOutOfOrder         = errors.New("out-of-order signaling state")
	ErrNegotiationTimeout = errors.New("negotiation timeout")
	ErrUnknownRequest     = errors.New("unknown signaling request")
	ErrAborted            = errors.New("signaling request aborted")
	ErrInvalidLink        = errors.New("invalid signaling link")
	ErrEmptySDP           = errors.New("empty session description")
)

// Phase is the negotiation phase of a single link.
type Phase string

// Link phases. A link only moves forward: idle -> offered -> answered.
const (
	PhaseIdle     Phase = "idle"
	PhaseOffered  Phase = "offered"
	PhaseAnswered Phase = "answered"
	PhaseClosed   Phase = "closed"
)

type linkState struct {
	phase   Phase
	offer   string
	answer  string
	changed chan struct{}
	err     error
}

func newLinkState() *linkState {
	return &linkState{phase: PhaseIdle, changed: make(chan struct{})}
}

func (l *linkState) notify() {
	close(l.changed)
	l.changed = make(chan struct{})
}

type requestState struct {
	id       int64
	deadline time.Time
	links    map[string]*linkState
	closed   bool
	err      error
	timer    *time.Timer
}

func (r *requestState) close(err error) {
	if r.closed {
		return
	}
	r.closed = true
	r.err = err
	for _, l := range r.links {
		l.phase = PhaseClosed
		l.err = err
		l.notify()
	}
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
}

// Store owns the signaling state machine. All mutation is serialized.
type Store struct {
	mu      sync.Mutex
	timeout time.Duration
	nextID  int64
	current *requestState
	now     func() time.Time
}

// NewStore returns a store whose rounds expire after timeout.
func NewStore(timeout time.Duration) *Store {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Store{timeout: timeout, now: time.Now}
}

// Timeout is the negotiation deadline applied to new rounds.
func (s *Store) Timeout() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.timeout
}

// Open starts a new request round and expires any unfinished previous round.
func (s *Store) Open() (int64, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != nil && !s.current.closed {
		s.current.close(ErrExpiredRequest)
	}
	s.nextID++
	st := &requestState{
		id:       s.nextID,
		deadline: s.now().Add(s.timeout),
		links: map[string]*linkState{
			LinkIngress: newLinkState(),
			LinkEgress:  newLinkState(),
		},
	}
	st.timer = time.AfterFunc(time.Until(st.deadline), func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.current == st {
			s.failLocked(st, ErrNegotiationTimeout)
		}
	})
	s.current = st
	return st.id, st.deadline
}
