package syncer

import (
	"sort"

	"mediacore"
)

// FrameSample is one decoded video sample's placement: its sample index,
// integer-nanosecond presentation time and keyframe flag.
type FrameSample struct {
	Index    int
	Time     int64
	Keyframe bool
}

// VFRMapper maps variable-frame-rate video samples onto the shared master
// time base. It validates the sample table produced by the MP4 parser,
// handling track start offsets, duplicate timestamps and empty tracks with
// explicit states rather than guessing frame durations.
type VFRMapper struct {
	track    mediacore.TrackID
	startOff int64 // track start offset relative to the timeline origin
	frames   []FrameSample
	sorted   bool

	// Duplicates counts samples sharing a presentation timestamp.
	Duplicates int
	// NonMonotonic counts timestamps that move backwards after de-duplication.
	NonMonotonic int
}

// NewVFRMapper builds a mapper. startOffsetNS shifts every sample onto the
// shared timeline origin.
func NewVFRMapper(track mediacore.TrackID, startOffsetNS int64) *VFRMapper {
	return &VFRMapper{track: track, startOff: startOffsetNS}
}

// Load validates a sample table. An empty table yields a classified empty
// error; duplicate timestamps are retained but counted; non-monotonic input
// is sorted deterministically and counted.
func (m *VFRMapper) Load(samples []FrameSample) error {
	if len(samples) == 0 {
		return mediacore.NewError(mediacore.CatEmpty, m.track, "vfr.load", 0,
			"empty video sample table")
	}
	seen := make(map[int64]int, len(samples))
	m.frames = make([]FrameSample, len(samples))
	for i, s := range samples {
		s.Time += m.startOff
		m.frames[i] = s
		if seen[s.Time] > 0 {
			m.Duplicates++
		}
		seen[s.Time]++
	}
	// Deterministic ordering: time first, then stable sample index.
	sort.SliceStable(m.frames, func(i, j int) bool {
		if m.frames[i].Time != m.frames[j].Time {
			return m.frames[i].Time < m.frames[j].Time
		}
		return m.frames[i].Index < m.frames[j].Index
	})
	for i := 1; i < len(m.frames); i++ {
		// Adjacent distinct times that would imply negative durations.
		if m.frames[i].Time < m.frames[i-1].Time {
			m.NonMonotonic++
		}
	}
	m.sorted = true
	return nil
}

// Frames returns the validated, time-ordered frame table.
func (m *VFRMapper) Frames() []FrameSample { return m.frames }

// FrameAt returns the frame whose presentation interval contains timeNS,
// using last-sample-wins at exact boundary times. ok=false precedes the first
// sample. Duration of a frame is the gap to the next sample; the final frame
// reuses the previous gap.
func (m *VFRMapper) FrameAt(timeNS int64) (FrameSample, bool) {
	if !m.sorted || len(m.frames) == 0 || timeNS < m.frames[0].Time {
		return FrameSample{}, false
	}
	idx := sort.Search(len(m.frames), func(i int) bool {
		return m.frames[i].Time > timeNS
	}) - 1
	return m.frames[idx], true
}

// DurationNS returns the presentation span from the first to the last sample
// plus the final frame's inferred duration.
func (m *VFRMapper) DurationNS() int64 {
	if len(m.frames) < 2 {
		return 0
	}
	gap := m.frames[len(m.frames)-1].Time - m.frames[len(m.frames)-2].Time
	return m.frames[len(m.frames)-1].Time - m.frames[0].Time + gap
}

// Keyframes returns the indexes of all keyframes in presentation order.
func (m *VFRMapper) Keyframes() []int {
	out := make([]int, 0)
	for _, f := range m.frames {
		if f.Keyframe {
			out = append(out, f.Index)
		}
	}
	return out
}

// Events converts the frame table into mediacore events on the shared time
// base, ready for timeline ingestion.
func (m *VFRMapper) Events() []mediacore.Event {
	out := make([]mediacore.Event, 0, len(m.frames))
	for _, f := range m.frames {
		out = append(out, mediacore.Event{
			Track:    m.track,
			Kind:     mediacore.KindVideo,
			Time:     f.Time,
			OrigTime: f.Time - m.startOff,
			Seq:      uint64(f.Index),
			Keyframe: f.Keyframe,
		})
	}
	return out
}
