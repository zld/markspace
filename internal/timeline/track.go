// Package timeline aligns multiple recovered tracks onto one monotonic
// output timeline with explicit placeholder rules.
package timeline

import "avcheck/internal/media"

// TrackState is the recovery state of one input track.
type TrackState uint8

const (
	TrackClean TrackState = iota
	TrackDegraded
	TrackRecovered
	TrackFailed
)

func (s TrackState) String() string {
	switch s {
	case TrackClean:
		return "clean"
	case TrackDegraded:
		return "degraded"
	case TrackRecovered:
		return "recovered"
	case TrackFailed:
		return "failed"
	}
	return "unknown"
}

// Track holds the frames of one stream that survived transport and ingest.
// Committed sequence numbers can never be rewritten.
type Track struct {
	ID    uint32
	State TrackState

	frames    map[uint64]*media.Frame
	committed map[uint64]struct{}
	minSeq    uint64
	maxSeq    uint64
	started   bool
}

// NewTrack builds an empty track.
func NewTrack(id uint32) *Track {
	return &Track{
		ID:        id,
		frames:    map[uint64]*media.Frame{},
		committed: map[uint64]struct{}{},
	}
}

// Add inserts a frame. Rewriting a committed sequence number is a state
// error; the frame is dropped and the track keeps its committed data.
func (t *Track) Add(f *media.Frame) error {
	if _, ok := t.committed[f.Seq]; ok {
		return media.NewError(media.KindState, f.StreamID, f.Seq, -1,
			"sequence already committed, refusing rewrite")
	}
	if _, dup := t.frames[f.Seq]; dup {
		return media.NewError(media.KindState, f.StreamID, f.Seq, -1, "duplicate frame sequence")
	}
	t.frames[f.Seq] = f
	if !t.started || f.Seq < t.minSeq {
		t.minSeq = f.Seq
	}
	if !t.started || f.Seq > t.maxSeq {
		t.maxSeq = f.Seq
	}
	t.started = true
	return nil
}

// MarkCommitted freezes a sequence number after composition.
func (t *Track) MarkCommitted(seq uint64) { t.committed[seq] = struct{}{} }

// Get returns the frame for a sequence number.
func (t *Track) Get(seq uint64) (*media.Frame, bool) {
	f, ok := t.frames[seq]
	return f, ok
}

// Range returns the [min, max] sequence span of the track.
func (t *Track) Range() (lo, hi uint64, ok bool) { return t.minSeq, t.maxSeq, t.started }

// Degrade records a fault observation; recovery moves the track to
// TrackRecovered, never back to clean, so downstream stages see a
// consistent history.
func (t *Track) Degrade() {
	if t.State == TrackClean {
		t.State = TrackDegraded
	}
}

// Recover notes that the track caught up after a degraded period.
func (t *Track) Recover() {
	if t.State == TrackDegraded {
		t.State = TrackRecovered
	}
}
