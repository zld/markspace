package clock

import "time"

// TimelineState is the checkpointable unwrap/origin state. Wall-clock report
// details are not needed for deterministic media-time manifests, but the fact
// that an SSRC had a sender report is retained.
type TimelineState struct {
	SampleRate  uint32                   `json:"sample_rate"`
	States      map[uint32]WrapStateJSON `json:"states"`
	ClockSource map[uint32]string        `json:"clock_source,omitempty"`
	HasReport   map[uint32]bool          `json:"has_report,omitempty"`
}

type WrapStateJSON struct {
	HighestExtTS  uint64 `json:"highest_ext_ts"`
	HaveTS        bool   `json:"have_ts"`
	OriginExtTS   uint64 `json:"origin_ext_ts"`
	HasOriginTS   bool   `json:"has_origin_ts"`
	HighestExtSeq uint64 `json:"highest_ext_seq"`
	HaveSeq       bool   `json:"have_seq"`
}

func (t *Timeline) State() TimelineState {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := TimelineState{
		SampleRate:  t.SampleRate,
		States:      make(map[uint32]WrapStateJSON, len(t.states)),
		ClockSource: make(map[uint32]string, len(t.clockSource)),
		HasReport:   make(map[uint32]bool, len(t.reports)),
	}
	for ssrc, w := range t.states {
		st.States[ssrc] = WrapStateJSON{
			HighestExtTS: w.highestExtTS, HaveTS: w.haveTS,
			OriginExtTS: w.originExtTS, HasOriginTS: w.hasOriginTS,
			HighestExtSeq: w.highestExtSeq, HaveSeq: w.haveSeq,
		}
	}
	for k, v := range t.clockSource {
		st.ClockSource[k] = v
	}
	for k := range t.reports {
		st.HasReport[k] = true
	}
	return st
}

func (t *Timeline) Restore(st TimelineState) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if st.SampleRate != 0 {
		t.SampleRate = st.SampleRate
	}
	t.states = make(map[uint32]*wrapState, len(st.States))
	for ssrc, w := range st.States {
		t.states[ssrc] = &wrapState{
			highestExtTS: w.HighestExtTS, haveTS: w.HaveTS,
			originExtTS: w.OriginExtTS, hasOriginTS: w.HasOriginTS,
			highestExtSeq: w.HighestExtSeq, haveSeq: w.HaveSeq,
		}
	}
	t.clockSource = make(map[uint32]string, len(st.ClockSource))
	for k, v := range st.ClockSource {
		t.clockSource[k] = v
	}
	t.reports = make(map[uint32]correction, len(st.HasReport))
	for ssrc := range st.HasReport {
		// Presence is enough to suppress the missing-report error after resume;
		// a later RTCP report repopulates the actual wall correction.
		t.reports[ssrc] = correction{receivedAt: time.Unix(0, 0)}
	}
}

func (t *Timeline) HasSenderReport(ssrc uint32) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.reports[ssrc]
	return ok
}
