package signal

import (
	"fmt"
	"sync"
	"time"
)

// State is the signaling state of one endpoint.
type State int

const (
	StateNew            State = iota
	StateOfferExchanged       // offer sent (offerer) or received (answerer)
	StateAnswered             // answer exchanged both ways
	StateICEGathering         // trickle ICE in progress
	StateReady                // end-of-candidates seen on both sides
	StateClosed
	StateFailed
)

func (s State) String() string {
	switch s {
	case StateNew:
		return "new"
	case StateOfferExchanged:
		return "offer_exchanged"
	case StateAnswered:
		return "answered"
	case StateICEGathering:
		return "ice_gathering"
	case StateReady:
		return "ready"
	case StateClosed:
		return "closed"
	case StateFailed:
		return "failed"
	}
	return "unknown"
}

// Role selects which message sequence an endpoint expects.
type Role int

const (
	Offerer Role = iota
	Answerer
)

// Endpoint tracks the signaling state machine of one side of a connection.
// It is purely local: the runner moves decoded envelopes between the two
// endpoints of a pair and each endpoint validates ordering, duplicates and
// timeouts.
type Endpoint struct {
	mu    sync.Mutex
	name  string
	role  Role
	state State
	// iceDoneDirs counts end-of-candidates markers seen; both directions
	// must signal it before the endpoint is ready.
	iceDoneDirs int
	err         error
	// lastActivity drives Stall(), which the runner polls to detect a
	// signaling exchange that stopped making progress.
	lastActivity time.Time
	now          func() time.Time
}

// NewEndpoint creates an endpoint in StateNew.
func NewEndpoint(name string, role Role) *Endpoint {
	return &Endpoint{name: name, role: role, state: StateNew, lastActivity: time.Now(), now: time.Now}
}

// State returns the current state.
func (e *Endpoint) State() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state
}

// Err returns the sticky failure reason, if any.
func (e *Endpoint) Err() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.err
}

// HandleMessage validates an incoming decoded envelope against the state
// machine and advances the state. Duplicate or out-of-state messages are
// hard errors and move the endpoint to StateFailed.
func (e *Endpoint) HandleMessage(env *Envelope) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state == StateFailed {
		return fmt.Errorf("signal[%s]: already failed: %w", e.name, e.err)
	}
	if e.state == StateClosed {
		return fmt.Errorf("signal[%s]: message %s on closed endpoint", e.name, env.Type)
	}

	expectSDP := func(want MessageType) error {
		if env.Type != want {
			return e.failLocked(fmt.Errorf("out-of-state message %s in state %s (want %s)", env.Type, e.state, want))
		}
		return nil
	}

	switch e.state {
	case StateNew:
		// The first message must be the offer, in either role: the offerer
		// records its own outgoing offer, the answerer the incoming one.
		if err := expectSDP(TypeOffer); err != nil {
			return err
		}
		e.state = StateOfferExchanged
	case StateOfferExchanged:
		if env.Type == TypeOffer {
			return e.failLocked(fmt.Errorf("duplicate offer"))
		}
		if err := expectSDP(TypeAnswer); err != nil {
			return err
		}
		e.state = StateAnswered
	case StateAnswered, StateICEGathering:
		switch env.Type {
		case TypeOffer, TypeAnswer:
			return e.failLocked(fmt.Errorf("duplicate %s in state %s", env.Type, e.state))
		case TypeICE:
			var p ICEPayload
			if err := DecodePayload(env, &p); err != nil {
				return e.failLocked(err)
			}
			e.state = StateICEGathering
			if p.Candidate == "" { // end-of-candidates marker
				e.iceDoneDirs++
				if e.iceDoneDirs >= 2 {
					e.state = StateReady
				}
			}
		case TypeClose:
			e.state = StateClosed
		}
	case StateReady:
		if env.Type == TypeClose {
			e.state = StateClosed
			break
		}
		return e.failLocked(fmt.Errorf("out-of-state message %s in state %s", env.Type, e.state))
	}
	e.lastActivity = e.now()
	return nil
}

// MarkReady is called by the runner once the transport reports connected,
// confirming signaling completed. Only valid from StateReady.
func (e *Endpoint) MarkReady() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state != StateReady {
		return e.failLocked(fmt.Errorf("cannot mark ready from state %s", e.state))
	}
	e.lastActivity = e.now()
	return nil
}

// Stall reports whether the endpoint has been stuck in a non-terminal
// state for longer than d.
func (e *Endpoint) Stall(d time.Duration) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch e.state {
	case StateReady, StateClosed, StateFailed:
		return false
	}
	return e.now().Sub(e.lastActivity) > d
}

// Close transitions the endpoint to StateClosed (idempotent) and releases
// its resources. A failed endpoint stays failed so its error is preserved.
func (e *Endpoint) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state != StateFailed {
		e.state = StateClosed
	}
	return nil
}

func (e *Endpoint) failLocked(err error) error {
	e.state = StateFailed
	e.err = err
	return fmt.Errorf("signal[%s]: %w", e.name, err)
}
