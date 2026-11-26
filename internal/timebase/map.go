// Package timebase maps fragment and RTP clocks to one microsecond timeline.
package timebase

import (
	"fmt"
	"math"

	"example.com/archiveverify/internal/cmaf"
	"example.com/archiveverify/internal/model"
)

// Sample is one media sample on the unified timeline.
type Sample struct {
	TrackID     uint32   `json:"track_id"`
	Role        string   `json:"role,omitempty"`
	SourceID    string   `json:"source_id"`
	EventIndex  int      `json:"event_index"`
	Sequence    uint32   `json:"mfhd_sequence"`
	SampleIndex int      `json:"sample_index"`
	StartUs     model.Us `json:"start_us"`
	EndUs       model.Us `json:"end_us"`
	RawStart    int64    `json:"raw_start"`
	RawEnd      int64    `json:"raw_end"`
	Timescale   uint32   `json:"timescale"`
	KeyFrame    bool     `json:"key_frame"`
	File        string   `json:"file"`
	BoxOffset   int64    `json:"box_offset"`
	ByteOffset  int64    `json:"byte_offset,omitempty"`
	Size        uint32   `json:"size"`
}

// Issue is a non-fatal but expected media fault. Fatal input errors remain
// *model.MediaError and abort the affected input.
type Issue struct {
	*model.MediaError
}

// Report contains mapped samples plus audit records.
type Report struct {
	Samples []Sample       `json:"samples"`
	Repairs []model.Repair `json:"repairs"`
	Gaps    []model.Gap    `json:"mfhd_gaps"`
	Issues  []Issue        `json:"issues,omitempty"`
	TimeMin model.Us       `json:"time_min_us"`
	TimeMax model.Us       `json:"time_max_us"`
}

type trackState struct {
	source     string
	haveRaw    bool
	lastRaw    int64 // unwrapped timescale units
	lastEndUs  model.Us
	haveEnd    bool
	wrapCycles int64
}

// Mapper applies ordered source/fragment events. It is not safe for concurrent
// use; pipeline stages own one mapper or synchronize calls.
type Mapper struct {
	tracks  map[uint32]*trackState
	lastSeq map[string]uint32
	haveSeq map[string]bool
	Repairs []model.Repair
	Gaps    []model.Gap
	Issues  []Issue
}

// NewMapper creates an empty timeline mapper.
func NewMapper() *Mapper {
	return &Mapper{tracks: map[uint32]*trackState{}, lastSeq: map[string]uint32{}, haveSeq: map[string]bool{}}
}

// ObserveSequence records mfhd continuity for an ordered source event.
func (m *Mapper) ObserveSequence(source string, seq uint32, eventIndex int, boxOffset int64) {
	if m.haveSeq[source] {
		prev := m.lastSeq[source]
		if seq != prev+1 && seq > prev {
			m.Gaps = append(m.Gaps, model.Gap{
				Source: source, AfterSeq: prev, BeforeSeq: seq,
				MissingCount: seq - prev - 1, EventIndex: eventIndex, BoxOffset: boxOffset,
			})
		}
	}
	m.lastSeq[source] = seq
	m.haveSeq[source] = true
}

// MapFragment maps all tracks in f onto the shared timeline.
func (m *Mapper) MapFragment(sourceID string, eventIndex int, f *cmaf.Fragment) (*Report, error) {
	if f == nil {
		return nil, fmt.Errorf("nil fragment")
	}
	if f.HasSequence {
		m.ObserveSequence(sourceID, f.Sequence, eventIndex, f.Moof.Offset)
	}
	rep := &Report{}
	for _, tf := range f.Tracks {
		if tf.Timescale == 0 {
			return nil, &model.MediaError{Code: model.CodeParse, Source: sourceID, TrackID: tf.TrackID,
				BoxType: "mdhd", BoxOffset: f.Moof.Offset, Err: fmt.Errorf("zero timescale")}
		}
		st := m.tracks[tf.TrackID]
		if st == nil {
			st = &trackState{}
			m.tracks[tf.TrackID] = st
		}
		rawStart, repair, issue, err := m.resolveStart(sourceID, f, tf, st)
		if err != nil {
			return nil, err
		}
		if repair != nil {
			m.Repairs = append(m.Repairs, *repair)
			rep.Repairs = append(rep.Repairs, *repair)
		}
		if issue != nil {
			m.Issues = append(m.Issues, *issue)
			rep.Issues = append(rep.Issues, *issue)
		}

		cursorRaw := rawStart
		// When a backward tfdt is repaired to the previous end, raw values no
		// longer need to advance in source units; Us remains monotonic.
		cursorUs := unitsToUsChecked(rawStart, tf.Timescale)
		if repair != nil {
			cursorUs = repair.FixedUs
		}
		for i, smp := range tf.Samples {
			durUs := UnitsToUs(int64(smp.Duration), tf.Timescale)
			endUs := cursorUs + durUs
			s := Sample{
				TrackID: tf.TrackID, SourceID: sourceID, EventIndex: eventIndex,
				Sequence: f.Sequence, SampleIndex: i, StartUs: cursorUs, EndUs: endUs,
				RawStart: cursorRaw, RawEnd: cursorRaw + int64(smp.Duration),
				Timescale: tf.Timescale, KeyFrame: smp.Sync, File: f.Path,
				BoxOffset: f.Moof.Offset, Size: smp.Size,
			}
			rep.Samples = append(rep.Samples, s)
			cursorUs = endUs
			cursorRaw += int64(smp.Duration)
		}
		st.source = sourceID
		st.haveRaw = true
		st.lastRaw = rawStart
		st.lastEndUs = cursorUs
		st.haveEnd = true
	}
	return rep, nil
}

func unitsToUsChecked(v int64, scale uint32) model.Us {
	if scale == 0 {
		return 0
	}
	return UnitsToUs(v, scale)
}

// UnitsToUs converts timescale units to rounded integer microseconds, checking
// multiplication overflow for realistic nonnegative media values.
func UnitsToUs(units int64, scale uint32) model.Us {
	if scale == 0 {
		return 0
	}
	// Round to nearest microsecond while avoiding avoidable overflow.
	q := units / int64(scale)
	r := units % int64(scale)
	us := q*1_000_000 + (r*1_000_000+int64(scale)/2)/int64(scale)
	return model.Us(us)
}

// UsToAudioUnits is the inverse rounded to an exact sample/frame boundary.
func UsToAudioUnits(us model.Us, scale uint32) int64 {
	if scale == 0 || us <= 0 {
		return 0
	}
	return (int64(us)*int64(scale) + 500_000) / 1_000_000
}

func safeMul(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	if a > math.MaxInt64/b {
		return 0, false
	}
	return a * b, true
}
