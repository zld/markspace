package signaling

import (
	"errors"
	"fmt"
	"sync"
)

// State is the signaling state machine state of a Peer.
type State int

const (
	StateIdle State = iota
	StateOffering
	StateAnswering
	StateEstablished
	StateClosed
)

func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateOffering:
		return "offering"
	case StateAnswering:
		return "answering"
	case StateEstablished:
		return "established"
	case StateClosed:
		return "closed"
	}
	return "unknown"
}

// Errors returned by the state machine. They are wrapped with context, so
// callers should use errors.Is.
var (
	ErrUnknownVersion    = errors.New("unknown signaling version")
	ErrDuplicateSeq      = errors.New("duplicate or non-increasing sequence")
	ErrSessionMismatch   = errors.New("session id mismatch")
	ErrIllegalTransition = errors.New("illegal signaling state transition")
	ErrPeerClosed        = errors.New("peer is closed")
)

// Peer is one endpoint of the local signaling state machine.
type Peer struct {
	name      string
	mu        sync.Mutex
	state     State
	sessionID string
	lastSeq   uint64
	hasSeq    bool
	inbox     chan Message
}

// NewPeer creates a peer in StateIdle. The inbox is buffered so senders do
// not block on the state machine.
func NewPeer(name string, inboxSize int) *Peer {
	if inboxSize < 1 {
		inboxSize = 1
	}
	return &Peer{name: name, state: StateIdle, inbox: make(chan Message, inboxSize)}
}

// Name returns the peer label for diagnostics.
func (p *Peer) Name() string { return p.name }

// State returns the current state.
func (p *Peer) State() State {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

// Inbox exposes the receive channel for the signaling pump.
func (p *Peer) Inbox() <-chan Message { return p.inbox }

// Deliver validates m against the envelope rules and the state machine and,
// on success, queues it for the peer's pump and applies the transition.
func (p *Peer) Deliver(m Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.state == StateClosed {
		return fmt.Errorf("%s: %w", p.name, ErrPeerClosed)
	}
	if err := m.Validate(); err != nil {
		var fe *FieldError
		if errors.As(err, &fe) && fe.Field == "version" {
			return fmt.Errorf("%s: %w: %v", p.name, ErrUnknownVersion, err)
		}
		return fmt.Errorf("%s: invalid message: %w", p.name, err)
	}
	if p.sessionID == "" {
		p.sessionID = m.SessionID
	} else if p.sessionID != m.SessionID {
		return fmt.Errorf("%s: %w: got %q want %q", p.name, ErrSessionMismatch, m.SessionID, p.sessionID)
	}
	if p.hasSeq && m.Seq <= p.lastSeq {
		return fmt.Errorf("%s: %w: seq %d after %d", p.name, ErrDuplicateSeq, m.Seq, p.lastSeq)
	}
	next, err := transition(p.state, m.Type)
	if err != nil {
		return fmt.Errorf("%s: %w: %s on %s", p.name, err, m.Type, p.state)
	}
	p.state = next
	p.lastSeq = m.Seq
	p.hasSeq = true
	p.inbox <- m
	return nil
}

// Close moves the peer to StateClosed and closes its inbox.
func (p *Peer) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state != StateClosed {
		p.state = StateClosed
		close(p.inbox)
	}
}

// transition maps (state, message type) to the next state.
func transition(s State, t MessageType) (State, error) {
	if t == TypeBye {
		return StateClosed, nil
	}
	switch s {
	case StateIdle:
		if t == TypeOffer {
			return StateAnswering, nil
		}
	case StateOffering:
		if t == TypeAnswer {
			return StateEstablished, nil
		}
		if t == TypeCandidate {
			return StateOffering, nil
		}
	case StateAnswering:
		if t == TypeCandidate {
			return StateAnswering, nil
		}
	case StateEstablished:
		if t == TypeCandidate {
			return StateEstablished, nil
		}
	}
	return s, ErrIllegalTransition
}

// MarkOffering moves an idle peer to StateOffering when it originates an
// offer (the offer itself is delivered to the remote peer).
func (p *Peer) MarkOffering(sessionID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state != StateIdle {
		return fmt.Errorf("%s: %w: offer from %s", p.name, ErrIllegalTransition, p.state)
	}
	p.state = StateOffering
	p.sessionID = sessionID
	return nil
}
