package timeline

import "sort"

// Edit is one edit-list entry expressed entirely on the unified axis.
// MediaStart is relative to media time zero; the container's non-zero media
// start timestamp is handled separately by Axis.StartOffset.
type Edit struct {
	MediaStart Time `json:"media_start_us"`
	Start      Time `json:"timeline_start_us"`
	Duration   Time `json:"duration_us"`
}

// End returns the exclusive timeline end of the edit.
func (e Edit) End() Time { return e.Start + e.Duration }

// MediaEnd returns the exclusive media-local end of the edit.
func (e Edit) MediaEnd() Time { return e.MediaStart + e.Duration }

// Axis is the single mapping authority between container media-local time and
// the unified presentation timeline. Every segment, silence interval and
// fingerprint record stores timeline coordinates produced by this type, so a
// non-zero media start offset and edit-list shifts can never silently diverge.
type Axis struct {
	// StartOffset is the media-local time of the earliest sample in the
	// container (the non-zero start timestamp, converted to microseconds).
	StartOffset Time
	edits       []Edit
}

// NewAxis creates an axis whose media starts at startOffset (zero for normal
// material).
func NewAxis(startOffset Time) *Axis {
	return &Axis{StartOffset: startOffset}
}

// AddEdit registers one edit-list entry. Edits must have positive duration and
// must not overlap on the timeline; media ranges may repeat (a repeated edit is
// legal and must still produce distinct timeline coordinates).
func (a *Axis) AddEdit(e Edit) error {
	if e.Duration <= 0 {
		return NewError(KindInvalidInput, "edit duration must be positive: %d", e.Duration)
	}
	if e.MediaStart < a.StartOffset {
		return NewError(KindInvalidInput,
			"edit media start %d precedes media start offset %d", e.MediaStart, a.StartOffset)
	}
	if e.Start < 0 {
		return NewError(KindInvalidInput, "negative timeline start %d", e.Start)
	}
	for _, ex := range a.edits {
		if e.Start < ex.End() && ex.Start < e.End() {
			return NewError(KindInvalidInput,
				"overlapping edits at timeline %d (existing %d..%d)", e.Start, ex.Start, ex.End())
		}
	}
	a.edits = append(a.edits, e)
	sort.SliceStable(a.edits, func(i, j int) bool { return a.edits[i].Start < a.edits[j].Start })
	return nil
}

// Edits returns the sorted edit-list entries.
func (a *Axis) Edits() []Edit {
	out := make([]Edit, len(a.edits))
	copy(out, a.edits)
	return out
}

// IdentityEdit appends a single edit presenting the whole media range
// [StartOffset, mediaEnd] from timeline zero. Used when the container carries
// no edit list.
func (a *Axis) IdentityEdit(mediaEnd Time) error {
	if mediaEnd <= a.StartOffset {
		return NewError(KindInvalidInput,
			"identity edit empty: media end %d <= start offset %d", mediaEnd, a.StartOffset)
	}
	return a.AddEdit(Edit{MediaStart: a.StartOffset, Start: 0, Duration: mediaEnd - a.StartOffset})
}

// ToTimeline maps a media-local time to the presentation timeline. The boolean
// is false when the time falls outside every presented edit: callers must treat
// such samples as not-presented rather than inventing a coordinate.
func (a *Axis) ToTimeline(mediaLocal Time) (Time, bool) {
	for _, e := range a.edits {
		if mediaLocal >= e.MediaStart && mediaLocal < e.MediaEnd() {
			t, err := Add(e.Start, mediaLocal-e.MediaStart)
			if err != nil {
				return 0, false
			}
			return t, true
		}
	}
	return 0, false
}

// ToMedia is the inverse mapping: timeline coordinate to media-local time.
func (a *Axis) ToMedia(t Time) (Time, bool) {
	for _, e := range a.edits {
		if t >= e.Start && t < e.End() {
			m, err := Add(e.MediaStart, t-e.Start)
			if err != nil {
				return 0, false
			}
			return m, true
		}
	}
	return 0, false
}

// Bounds returns the timeline [start, end) actually presented by the edit list.
func (a *Axis) Bounds() (Time, Time) {
	if len(a.edits) == 0 {
		return 0, 0
	}
	return a.edits[0].Start, a.edits[len(a.edits)-1].End()
}

// TimelineDuration is the presented duration.
func (a *Axis) TimelineDuration() Time {
	var d Time
	for _, e := range a.edits {
		d += e.Duration
	}
	return d
}
