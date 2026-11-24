package syncer

import (
	"medcheck/internal/clock"
	"medcheck/internal/recovery"
)

// DefaultDriftThresholdNS is the 80 ms AV-sync correction threshold.
const DefaultDriftThresholdNS int64 = 80_000_000

// Frame is a media event with monotonic synchronized timestamps.
type Frame struct {
	recovery.MediaEvent
	RawPTSNS      int64 `json:"raw_pts_ns"`
	AdjustedPTSNS int64 `json:"pts_ns"`
}

// DriftRecord describes one threshold crossing and its applied correction.
type DriftRecord struct {
	AtNS         int64 `json:"at_ns"`
	AudioPTSNS   int64 `json:"audio_pts_ns"`
	VideoPTSNS   int64 `json:"video_pts_ns"`
	DriftNS      int64 `json:"drift_ns"`
	CorrectionNS int64 `json:"correction_ns"`
}

// Stats contains synchronization counters.
type Stats struct {
	Frames      int     `json:"frames"`
	Corrections int     `json:"corrections"`
	LastDriftNS int64   `json:"last_drift_ns"`
	MaxDriftNS  int64   `json:"max_drift_ns"`
	AvDriftMS   float64 `json:"av_drift_ms"`
}

// Syncer uses audio as the master clock and corrects video offset.
type Syncer struct {
	ThresholdNS int64
	Stats       Stats
	Drifts      []DriftRecord

	latestRaw     map[clock.StreamID]int64
	offsetNS      map[clock.StreamID]int64
	lastAdjusted  map[clock.StreamID]int64
	haveAdjusted  map[clock.StreamID]bool
	overThreshold bool
}

// New creates an AV synchronizer.
func New() *Syncer {
	return &Syncer{
		ThresholdNS:  DefaultDriftThresholdNS,
		latestRaw:    make(map[clock.StreamID]int64),
		offsetNS:     make(map[clock.StreamID]int64),
		lastAdjusted: make(map[clock.StreamID]int64),
		haveAdjusted: make(map[clock.StreamID]bool),
	}
}

// Push aligns one recovered event and returns the monotonic frame.
func (s *Syncer) Push(ev recovery.MediaEvent) Frame {
	s.Stats.Frames++
	s.latestRaw[ev.Stream] = ev.PTSNS
	s.updateDrift(ev.PTSNS)

	raw := ev.PTSNS
	adj := raw + s.offsetNS[ev.Stream]
	if s.haveAdjusted[ev.Stream] && adj < s.lastAdjusted[ev.Stream] {
		adj = s.lastAdjusted[ev.Stream]
	}
	s.lastAdjusted[ev.Stream] = adj
	s.haveAdjusted[ev.Stream] = true

	ev.PTSNS = adj
	return Frame{MediaEvent: ev, RawPTSNS: raw, AdjustedPTSNS: adj}
}

func (s *Syncer) updateDrift(atNS int64) {
	a, haveA := s.latestRaw[clock.StreamAudio]
	v, haveV := s.latestRaw[clock.StreamVideo]
	if !haveA || !haveV {
		return
	}
	drift := a - v
	abs := abs64(drift)
	s.Stats.LastDriftNS = drift
	if abs > s.Stats.MaxDriftNS {
		s.Stats.MaxDriftNS = abs
		// Round to whole milliseconds for stable terminal output.
		s.Stats.AvDriftMS = float64((abs + 500_000) / 1_000_000)
	}
	if abs <= s.ThresholdNS {
		s.overThreshold = false
		return
	}
	if s.overThreshold {
		return
	}
	s.overThreshold = true

	// Audio is master. Move video's adjusted clock to the audio clock.
	oldOffset := s.offsetNS[clock.StreamVideo]
	newOffset := a - v
	s.offsetNS[clock.StreamVideo] = newOffset
	s.Stats.Corrections++
	s.Drifts = append(s.Drifts, DriftRecord{
		AtNS: atNS, AudioPTSNS: a, VideoPTSNS: v,
		DriftNS: drift, CorrectionNS: newOffset - oldOffset,
	})
}

// State is the checkpointable syncer state.
type State struct {
	LatestRaw     map[clock.StreamID]int64
	Offsets       map[clock.StreamID]int64
	LastAdjusted  map[clock.StreamID]int64
	HaveAdjusted  map[clock.StreamID]bool
	OverThreshold bool
	Stats         Stats
}

// Export returns checkpoint state.
func (s *Syncer) Export() State {
	return State{
		LatestRaw: s.latestRaw, Offsets: s.offsetNS, LastAdjusted: s.lastAdjusted,
		HaveAdjusted: s.haveAdjusted, OverThreshold: s.overThreshold, Stats: s.Stats,
	}
}

// Restore loads checkpoint state.
func (s *Syncer) Restore(st State) {
	if st.LatestRaw != nil {
		s.latestRaw = st.LatestRaw
	}
	if st.Offsets != nil {
		s.offsetNS = st.Offsets
	}
	if st.LastAdjusted != nil {
		s.lastAdjusted = st.LastAdjusted
	}
	if st.HaveAdjusted != nil {
		s.haveAdjusted = st.HaveAdjusted
	}
	s.overThreshold = st.OverThreshold
	s.Stats = st.Stats
}

// Offset returns the current offset for a stream.
func (s *Syncer) Offset(stream clock.StreamID) int64 {
	return s.offsetNS[stream]
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
