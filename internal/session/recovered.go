package session

import "fmt"

// recoveredSet remembers sequence numbers that arrived out of order so the
// frame can be marked recovered when it is finally emitted. Entries are
// removed on emit and pruned once they fall a bounded distance behind the
// committed cursor, so memory tracks the reorder window, not the stream.
type recoveredSet struct {
	marks map[uint16]struct{}
	ring  []uint16
	at    int
	n     int
}

func newRecoveredSet(window int) *recoveredSet {
	if window < 1 {
		window = 1
	}
	size := 4 * window
	if size < 16 {
		size = 16
	}
	return &recoveredSet{marks: map[uint16]struct{}{}, ring: make([]uint16, size)}
}

func (s *recoveredSet) add(seq uint16) {
	if _, ok := s.marks[seq]; ok {
		return
	}
	if s.n >= len(s.ring) {
		delete(s.marks, s.ring[s.at])
	} else {
		s.n++
	}
	s.ring[s.at] = seq
	s.at = (s.at + 1) % len(s.ring)
	s.marks[seq] = struct{}{}
}

// take reports whether seq was out of order and forgets it either way.
func (s *recoveredSet) take(seq uint16) bool {
	_, ok := s.marks[seq]
	delete(s.marks, seq)
	return ok
}

func (s *recoveredSet) String() string { return fmt.Sprintf("%d pending", len(s.marks)) }
