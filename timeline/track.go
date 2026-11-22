package timeline

import (
	"fmt"
	"sort"
)

// RTPSeqAbsent marks a block that was not transported over RTP.
const RTPSeqAbsent = 0xFFFF

// Block is one submitted raw sample chunk positioned on the integer grid.
type Block struct {
	// Sequence is the monotonic submission index of the block on its track.
	Sequence int
	// Interval is the half-open sample range the block covers.
	Interval Interval
	// PayloadBytes is the raw PCM payload size for size invariants.
	PayloadBytes int
	// RTPSequence is the source RTP sequence number; RTPSeqAbsent if none.
	RTPSequence uint16
}

// Checkpoint marks how far a track has durably processed. It is the unit of
// recovery: a rerun skips every block before Position and resumes after it.
type Checkpoint struct {
	TrackID    string `json:"track_id"`
	BlockIndex int    `json:"block_index"`
	Position   int64  `json:"position_samples"`
	Nanos      int64  `json:"position_nanos"`
}

// Track is one incoming multi-channel stream.
type Track struct {
	ID         string
	Rate       uint32
	Channels   uint16
	Layout     string
	DependsOn  []string
	Expected   Interval // declared total coverage; zero End means undeclared
	Blocks     []Block
	Checkpoint Checkpoint
}

// Coverage returns the span from the first block start to the last block end.
func (t *Track) Coverage() Interval {
	if len(t.Blocks) == 0 {
		return Interval{}
	}
	return Interval{Start: t.Blocks[0].Interval.Start, End: t.Blocks[len(t.Blocks)-1].Interval.End}
}

// Gaps returns uncovered sample ranges, including an early finish relative to
// the declared Expected interval. Gaps are not hard validation errors by
// themselves: downstream consumers decide whether a gap is fatal.
func (t *Track) Gaps() []Interval {
	var gaps []Interval
	for i := 1; i < len(t.Blocks); i++ {
		if g, ok := t.Blocks[i-1].Interval.Gap(t.Blocks[i].Interval); ok {
			gaps = append(gaps, g)
		}
	}
	if t.Expected.End > 0 {
		cov := t.Coverage()
		if len(t.Blocks) == 0 {
			gaps = append(gaps, t.Expected)
		} else if cov.End < t.Expected.End {
			gaps = append(gaps, Interval{Start: cov.End, End: t.Expected.End})
		}
	}
	return gaps
}

// Validate enforces per-track ordering and boundary rules. It never mutates
// the block slice: out-of-order submissions are reported, not silently sorted.
func (t *Track) Validate() error {
	if t.ID == "" {
		return NewError(ErrCodeIllegalBlockLength, StageTimeline, "", "track id must not be empty")
	}
	if t.Channels == 0 {
		return NewError(ErrCodeIllegalBlockLength, StageTimeline, t.ID, "channel count must be non-zero")
	}
	if _, err := NewClock(t.Rate); err != nil {
		pe, _ := AsError(err)
		return NewError(pe.Code, StageTimeline, t.ID, "%s", pe.Msg)
	}
	hasExpected := t.Expected.End > t.Expected.Start || t.Expected.Start != 0 || t.Expected.End != 0
	for i, b := range t.Blocks {
		if err := b.Interval.Validate(t.ID); err != nil {
			return err
		}
		if b.PayloadBytes < 0 {
			return NewError(ErrCodeIllegalBlockLength, StageTimeline, t.ID,
				"block %d has negative payload size %d", b.Sequence, b.PayloadBytes)
		}
		if hasExpected && !t.Expected.Contains(b.Interval) {
			return NewError(ErrCodeOutOfBounds, StageTimeline, t.ID,
				"block %d interval [%d,%d) escapes expected [%d,%d)",
				b.Sequence, b.Interval.Start, b.Interval.End, t.Expected.Start, t.Expected.End)
		}
		if i == 0 {
			continue
		}
		prev := t.Blocks[i-1].Interval
		switch {
		case b.Interval.Start == prev.Start:
			return NewError(ErrCodeDuplicateTimestamp, StageTimeline, t.ID,
				"block %d repeats start timestamp %d", b.Sequence, b.Interval.Start)
		case b.Interval.Start < prev.Start:
			return NewError(ErrCodeTimestampBackwards, StageTimeline, t.ID,
				"block %d start %d is before previous start %d",
				b.Sequence, b.Interval.Start, prev.Start)
		case b.Interval.Overlaps(prev):
			return NewError(ErrCodeOverlap, StageTimeline, t.ID,
				"block %d [%d,%d) overlaps previous [%d,%d)",
				b.Sequence, b.Interval.Start, b.Interval.End, prev.Start, prev.End)
		}
	}
	return nil
}

// TrackSet is the validated collection of all incoming tracks.
type TrackSet struct {
	Clock  Clock
	Tracks map[string]*Track
	Order  []string // stable submission order
}

// NewTrackSet builds and validates a set, keeping the given clock as the
// common output timeline.
func NewTrackSet(clock Clock, tracks []*Track) (*TrackSet, error) {
	set := &TrackSet{Clock: clock, Tracks: make(map[string]*Track, len(tracks))}
	for _, t := range tracks {
		if _, dup := set.Tracks[t.ID]; dup {
			return nil, NewError(ErrCodeDuplicateTimestamp, StageTimeline, t.ID,
				"duplicate track id %q", t.ID)
		}
		if err := t.Validate(); err != nil {
			return nil, err
		}
		set.Tracks[t.ID] = t
		set.Order = append(set.Order, t.ID)
	}
	return set, nil
}

// SortedOrder returns track ids in stable submission order.
func (s *TrackSet) SortedOrder() []string {
	out := append([]string(nil), s.Order...)
	sort.Strings(out)
	return out
}

// MissingBlocks reports every gap on a track as located errors.
func (s *TrackSet) MissingBlocks(trackID string) []*Error {
	t := s.Tracks[trackID]
	if t == nil {
		return nil
	}
	gaps := t.Gaps()
	errs := make([]*Error, 0, len(gaps))
	for _, g := range gaps {
		errs = append(errs, NewError(ErrCodeMissingBlock, StageTimeline, trackID,
			"missing %d samples in [%d,%d)", g.Length(), g.Start, g.End))
	}
	return errs
}

// String renders a short human-readable summary for CLI output.
func (c Checkpoint) String() string {
	return fmt.Sprintf("checkpoint{%s block=%d pos=%d}", c.TrackID, c.BlockIndex, c.Position)
}
