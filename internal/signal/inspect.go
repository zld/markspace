package signal

import (
	"time"
)

// LinkSnapshot is the inspectable state of one link.
type LinkSnapshot struct {
	Link   string `json:"link"`
	Phase  Phase  `json:"phase"`
	Offer  bool   `json:"offer_received"`
	Answer bool   `json:"answer_sent"`
}

// Snapshot is the inspectable state of a request round.
type Snapshot struct {
	RequestID int64          `json:"request_id"`
	Deadline  time.Time      `json:"deadline"`
	Closed    bool           `json:"closed"`
	Error     string         `json:"error,omitempty"`
	Links     []LinkSnapshot `json:"links"`
}

// Abort terminates a round, waking any waiters with ErrAborted.
func (s *Store) Abort(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil || s.current.id != id {
		return ErrUnknownRequest
	}
	if s.current.closed {
		return nil
	}
	s.current.close(ErrAborted)
	return nil
}

// Finish marks a completed round so its deadline no longer applies.
func (s *Store) Finish(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil || s.current.id != id || s.current.closed {
		return
	}
	s.current.close(nil)
}

// Snapshot returns the current round state, or nil when no round exists.
func (s *Store) Snapshot() *Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return nil
	}
	st := s.current
	s.expireLocked(st)
	out := &Snapshot{RequestID: st.id, Deadline: st.deadline, Closed: st.closed}
	if st.err != nil {
		out.Error = st.err.Error()
	}
	for _, name := range []string{LinkIngress, LinkEgress} {
		ls := st.links[name]
		out.Links = append(out.Links, LinkSnapshot{
			Link:   name,
			Phase:  ls.phase,
			Offer:  ls.offer != "",
			Answer: ls.answer != "",
		})
	}
	return out
}

func (s *Store) lookupLocked(id int64, link, sdp string) (*requestState, *linkState, error) {
	st, ls, err := s.lookupLockedRaw(id, link)
	if err != nil {
		return nil, nil, err
	}
	if sdp == "" {
		return nil, nil, ErrEmptySDP
	}
	if st.closed {
		if st.err != nil {
			return nil, nil, st.err
		}
		return nil, nil, ErrOutOfOrder
	}
	return st, ls, nil
}

func (s *Store) lookupLockedRaw(id int64, link string) (*requestState, *linkState, error) {
	if link != LinkIngress && link != LinkEgress {
		return nil, nil, ErrInvalidLink
	}
	if s.current == nil || s.current.id != id {
		if s.current != nil && id < s.current.id {
			return nil, nil, ErrExpiredRequest
		}
		return nil, nil, ErrUnknownRequest
	}
	return s.current, s.current.links[link], nil
}

// expireLocked fails a round whose negotiation deadline has passed.
func (s *Store) expireLocked(st *requestState) error {
	if st.closed {
		return nil
	}
	if !s.now().Before(st.deadline) {
		s.failLocked(st, ErrNegotiationTimeout)
		return ErrNegotiationTimeout
	}
	return nil
}

func (s *Store) failLocked(st *requestState, err error) {
	if st.closed {
		return
	}
	st.close(err)
}
