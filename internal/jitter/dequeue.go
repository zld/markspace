package jitter

// Peek returns the packet at the play cursor without removing it.
func (q *OrderedQueue) Peek(streamID uint16) (Store, bool) {
	s, ok := q.streams[streamID]
	if !ok {
		return Store{}, false
	}
	st, ok := s.buf[s.playSeq]
	return st, ok
}

// Pop removes and returns the packet at the play cursor, advancing the cursor.
// It returns false when the next frame is missing, leaving the cursor in place
// for the controller to decide whether to wait.
func (q *OrderedQueue) Pop(streamID uint16) (Store, bool) {
	s, ok := q.streams[streamID]
	if !ok {
		return Store{}, false
	}
	st, ok := s.buf[s.playSeq]
	if !ok {
		return Store{}, false
	}
	delete(s.buf, s.playSeq)
	q.recordPlayed(s, s.playSeq)
	s.playSeq++
	return st, true
}

// recordPlayed remembers a delivered sequence so a later copy is classified as
// a duplicate. The history is bounded to twice the queue capacity.
func (q *OrderedQueue) recordPlayed(s *streamQueue, seq uint32) {
	s.played[seq] = struct{}{}
	s.playedOrder = append(s.playedOrder, seq)
	if len(s.playedOrder) > 2*q.cfg.Capacity {
		old := s.playedOrder[0]
		s.playedOrder = s.playedOrder[1:]
		delete(s.played, old)
	}
}

// Advance moves the play cursor forward one frame without returning a packet.
// It is used after a concealed frame. Concealed slots are deliberately not
// recorded in the played history: a packet that arrives for a slot already
// concealed is late, not a duplicate.
func (q *OrderedQueue) Advance(streamID uint16) {
	if s, ok := q.streams[streamID]; ok {
		s.playSeq++
	}
}

// Cleanup drops any packet that fell behind the play cursor. In normal playout
// this is empty, but it keeps the queue bounded if a cursor is advanced
// without a pop.
func (q *OrderedQueue) Cleanup(streamID uint16) {
	s, ok := q.streams[streamID]
	if !ok {
		return
	}
	for seq := range s.buf {
		if int32(seq-s.playSeq) < 0 {
			delete(s.buf, seq)
		}
	}
}

// Resync advances the play cursor to the lowest buffered sequence, reporting
// how many sequence slots were skipped. Skipped slots were never played and
// are the caller's responsibility to classify (late packets after an
// interruption). It returns 0 when nothing is buffered.
func (q *OrderedQueue) Resync(streamID uint16) uint32 {
	s, ok := q.streams[streamID]
	if !ok || len(s.buf) == 0 {
		return 0
	}
	var lowest uint32
	var found bool
	for seq := range s.buf {
		if !found || int32(seq-lowest) < 0 {
			lowest, found = seq, true
		}
	}
	if !found {
		return 0
	}
	skipped := uint32(lowest - s.playSeq)
	s.playSeq = lowest
	return skipped
}

// NextSeq returns the sequence the play cursor expects next.
func (q *OrderedQueue) NextSeq(streamID uint16) (uint32, bool) {
	s, ok := q.streams[streamID]
	if !ok {
		return 0, false
	}
	return s.playSeq, true
}

// Buffered reports how many packets are buffered for one stream.
func (q *OrderedQueue) Buffered(streamID uint16) int {
	if s, ok := q.streams[streamID]; ok {
		return len(s.buf)
	}
	return 0
}

// Size reports the number of buffered packets across all streams.
func (q *OrderedQueue) Size() int {
	n := 0
	for _, s := range q.streams {
		n += len(s.buf)
	}
	return n
}

// BufferedFrames reports buffered media frames across all streams. It differs
// from Size only when packets carry more than one frame.
func (q *OrderedQueue) BufferedFrames() int {
	n := 0
	for _, s := range q.streams {
		for _, st := range s.buf {
			frames := int(st.Frames)
			if frames < 1 {
				frames = 1
			}
			n += frames
		}
	}
	return n
}

// StreamCount reports the number of tracked streams.
func (q *OrderedQueue) StreamCount() int { return len(q.streams) }

// Stats returns a snapshot including current occupancy.
func (q *OrderedQueue) Stats() QueueStats {
	st := q.stats
	st.Buffered = q.Size()
	st.Streams = len(q.streams)
	return st
}
