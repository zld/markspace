package jitter

import (
	"errors"
	"slices"
)

// Frame is one queued audio frame.
type Frame struct {
	Seq       uint16
	Timestamp uint32
	PCM       []int16
}

var (
	ErrFull      = errors.New("jitter: queue full")
	ErrDuplicate = errors.New("jitter: duplicate sequence")
	ErrConflict  = errors.New("jitter: same sequence, different payload")
)

// SeqLess reports whether a precedes b in 16-bit wrap-around order.
func SeqLess(a, b uint16) bool { return int16(a-b) < 0 }

// Queue is a bounded sequence-ordered frame buffer. The bound caps both
// memory and the number of frames a single burst can hold hostage.
type Queue struct {
	frames []Frame
	max    int
}

func NewQueue(max int) *Queue {
	if max < 1 {
		max = 1
	}
	return &Queue{max: max}
}

func (q *Queue) Len() int { return len(q.frames) }
func (q *Queue) Cap() int { return q.max }

// Insert places f in sequence order. A frame whose sequence already exists
// is a duplicate (identical payload) or a conflict (different payload).
func (q *Queue) Insert(f Frame) error {
	pos := 0
	for pos < len(q.frames) && SeqLess(q.frames[pos].Seq, f.Seq) {
		pos++
	}
	if pos < len(q.frames) && q.frames[pos].Seq == f.Seq {
		if slices.Equal(q.frames[pos].PCM, f.PCM) {
			return ErrDuplicate
		}
		return ErrConflict
	}
	if len(q.frames) >= q.max {
		return ErrFull
	}
	q.frames = append(q.frames, Frame{})
	copy(q.frames[pos+1:], q.frames[pos:])
	q.frames[pos] = f
	return nil
}

// Peek returns the earliest frame without removing it.
func (q *Queue) Peek() (Frame, bool) {
	if len(q.frames) == 0 {
		return Frame{}, false
	}
	return q.frames[0], true
}

// Pop removes and returns the earliest frame.
func (q *Queue) Pop() (Frame, bool) {
	if len(q.frames) == 0 {
		return Frame{}, false
	}
	f := q.frames[0]
	q.frames = q.frames[1:]
	return f, true
}

// DropBefore removes every frame whose sequence precedes ref (frames past
// their playout deadline) and returns how many were dropped.
func (q *Queue) DropBefore(ref uint16) int {
	n := 0
	for n < len(q.frames) && SeqLess(q.frames[n].Seq, ref) {
		n++
	}
	q.frames = q.frames[n:]
	return n
}
