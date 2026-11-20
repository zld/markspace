package jitter

import (
	"math"
	"time"

	"jitterlab/internal/packet"
)

// QueueConfig bounds queue memory. Capacity is the maximum number of packets
// buffered per stream; Queues bounds the number of tracked streams;
// RegressionMS is how far a capture timestamp may fall behind the newest
// accepted timestamp before the packet is treated as a clock regression rather
// than ordinary reordering.
type QueueConfig struct {
	Capacity     int
	Queues       int
	RegressionMS uint32
}

// DefaultQueueConfig keeps roughly 1.3 seconds of 20 ms audio per stream.
func DefaultQueueConfig() QueueConfig {
	return QueueConfig{Capacity: 64, Queues: 32, RegressionMS: 5000}
}

func (c QueueConfig) normalized() QueueConfig {
	if c.Capacity < 8 {
		c.Capacity = 8
	}
	if c.Queues < 1 {
		c.Queues = 1
	}
	if c.RegressionMS == 0 {
		c.RegressionMS = 5000
	}
	return c
}

// QueueStats breaks down packet disposition. Exactly one counter advances per
// Push call that does not store the packet.
type QueueStats struct {
	Accepted   int64 `json:"packets_accepted"`
	Duplicate  int64 `json:"drop_duplicate"`
	Late       int64 `json:"drop_late"`
	Overflow   int64 `json:"drop_overflow"`
	Ambiguous  int64 `json:"drop_ambiguous"`
	Regression int64 `json:"drop_timestamp_regression"`
	StreamDrop int64 `json:"drop_stream_limit"`
	Buffered   int   `json:"buffered"`
	Streams    int   `json:"streams"`
}

// OrderedQueue buffers packets by (stream, sequence). Sequence comparison uses
// unsigned wraparound arithmetic relative to each stream's play cursor.
type OrderedQueue struct {
	cfg     QueueConfig
	streams map[uint16]*streamQueue
	stats   QueueStats
}

// NewQueue creates an empty queue set.
func NewQueue(cfg QueueConfig) *OrderedQueue {
	return &OrderedQueue{cfg: cfg.normalized(), streams: make(map[uint16]*streamQueue)}
}

// Push accepts or rejects a validated packet. The matching QueueStats counter
// explains every rejection.
func (q *OrderedQueue) Push(p *packet.Packet, arrival time.Duration) bool {
	s, ok := q.streams[p.Header.StreamID]
	if !ok {
		if len(q.streams) >= q.cfg.Queues {
			q.stats.StreamDrop++
			return false
		}
		s = &streamQueue{
			buf:    make(map[uint32]Store),
			played: make(map[uint32]struct{}),
		}
		q.streams[p.Header.StreamID] = s
	}

	seq := p.Header.Seq
	if _, ok := s.played[seq]; ok {
		q.stats.Duplicate++
		return false
	}
	if !s.started {
		s.started = true
		s.playSeq = seq
		s.buf[seq] = newStore(p, arrival)
		s.lastStamp = p.Header.Timestamp
		q.stats.Accepted++
		return true
	}

	delta := int32(seq - s.playSeq)
	switch {
	case delta == math.MinInt32:
		// Exactly half the sequence space away: new/old cannot be decided.
		q.stats.Ambiguous++
		return false
	case delta < 0:
		q.stats.Late++
		return false
	case delta >= int32(q.cfg.Capacity):
		q.stats.Overflow++
		return false
	}

	if _, ok := s.buf[seq]; ok {
		q.stats.Duplicate++
		return false
	}
	if behind := int32(s.lastStamp - p.Header.Timestamp); behind > 0 &&
		int64(behind) > int64(q.cfg.RegressionMS) {
		// Far ahead of playout but carrying a much older capture time: the
		// sender's clock regressed or the stream restarted.
		q.stats.Regression++
		return false
	}

	if len(s.buf) >= q.cfg.Capacity {
		q.evictOldest(s)
		q.stats.Overflow++
	}
	s.buf[seq] = newStore(p, arrival)
	if int32(p.Header.Timestamp-s.lastStamp) > 0 {
		s.lastStamp = p.Header.Timestamp
	}
	q.stats.Accepted++
	return true
}

// evictOldest removes the lowest buffered sequence so the newest packet can be
// stored. Every eviction is counted as overflow by the caller.
func (q *OrderedQueue) evictOldest(s *streamQueue) {
	var oldest uint32
	var found bool
	for seq := range s.buf {
		if !found || int32(seq-oldest) < 0 {
			oldest, found = seq, true
		}
	}
	if found {
		delete(s.buf, oldest)
	}
}

// StreamIDs returns tracked stream ids in ascending order.
func (q *OrderedQueue) StreamIDs() []uint16 {
	ids := make([]uint16, 0, len(q.streams))
	for id := range q.streams {
		ids = append(ids, id)
	}
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
	return ids
}
