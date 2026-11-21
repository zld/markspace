package signal

import (
	"context"
)

// PutOffer records an offer for one link of the given round.
func (s *Store) PutOffer(id int64, link, sdp string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ls, err := s.lookupLocked(id, link, sdp)
	if err != nil {
		return err
	}
	if err := s.expireLocked(st); err != nil {
		return err
	}
	switch ls.phase {
	case PhaseIdle:
		ls.offer = sdp
		ls.phase = PhaseOffered
		ls.notify()
		return nil
	case PhaseOffered, PhaseAnswered:
		if ls.offer == sdp {
			s.failLocked(st, ErrDuplicateRequest)
			return ErrDuplicateRequest
		}
		s.failLocked(st, ErrOutOfOrder)
		return ErrOutOfOrder
	default:
		s.failLocked(st, ErrOutOfOrder)
		return ErrOutOfOrder
	}
}

// PutAnswer records an answer for one link of the given round.
func (s *Store) PutAnswer(id int64, link, sdp string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ls, err := s.lookupLocked(id, link, sdp)
	if err != nil {
		return err
	}
	if err := s.expireLocked(st); err != nil {
		return err
	}
	switch ls.phase {
	case PhaseOffered:
		ls.answer = sdp
		ls.phase = PhaseAnswered
		ls.notify()
		return nil
	case PhaseAnswered:
		if ls.answer == sdp {
			s.failLocked(st, ErrDuplicateRequest)
			return ErrDuplicateRequest
		}
		s.failLocked(st, ErrOutOfOrder)
		return ErrOutOfOrder
	default:
		s.failLocked(st, ErrOutOfOrder)
		return ErrOutOfOrder
	}
}

// AwaitOffer blocks until the link has an offer or the round terminates.
func (s *Store) AwaitOffer(ctx context.Context, id int64, link string) (string, error) {
	return s.await(ctx, id, link, PhaseOffered, true)
}

// AwaitAnswer blocks until the link has an answer or the round terminates.
func (s *Store) AwaitAnswer(ctx context.Context, id int64, link string) (string, error) {
	return s.await(ctx, id, link, PhaseAnswered, false)
}

func (s *Store) await(ctx context.Context, id int64, link string, want Phase, offer bool) (string, error) {
	for {
		s.mu.Lock()
		st, ls, err := s.lookupLockedRaw(id, link)
		if err != nil {
			s.mu.Unlock()
			return "", err
		}
		if serr := s.expireLocked(st); serr != nil {
			s.mu.Unlock()
			return "", serr
		}
		if st.closed {
			err := st.err
			s.mu.Unlock()
			return "", err
		}
		ready := ls.phase == want || (want == PhaseOffered && ls.phase == PhaseAnswered)
		if ready {
			var out string
			if offer {
				out = ls.offer
			} else {
				out = ls.answer
			}
			s.mu.Unlock()
			if out == "" {
				return "", ErrEmptySDP
			}
			return out, nil
		}
		ch := ls.changed
		s.mu.Unlock()

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ch:
		}
	}
}
