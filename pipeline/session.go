package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"mediacore/media"
)

// EventKind tags a scheduled timeline event.
type EventKind uint8

const (
	EventAudio EventKind = iota
	EventVideo
	EventRecovery
)

// Event is one unit scheduled onto the unified timeline.
type Event struct {
	TrackID       uint32
	Kind          EventKind
	Seq           int64
	PTSNanos      int64 // presentation time on the unified axis
	DurationNanos int64
	// LatencyNanos is the deterministic enqueue-to-emit delay measured by the
	// caller's virtual clock; the session only aggregates it.
	LatencyNanos int64
	// SyncRefNanos is the paired other-track PTS used for A/V sync error.
	SyncRefNanos int64
	HasSyncRef   bool
	Incomplete   bool // declared frame whose payload is missing
	Fingerprint  []byte
}

type trackState struct {
	lastPTS   int64
	lastEnd   int64
	haveEvent bool
	samples   int64
	submitted int64
}

// Session schedules tracks onto one timeline, enforces monotonicity and
// sample-count invariants, aggregates deterministic metrics and maintains a
// serialisable hash-chain checkpoint.
type Session struct {
	tracks      map[uint32]*trackState
	syncErrors  []int64 // absolute nanoseconds
	latencies   []int64 // nanoseconds
	chain       [32]byte
	chainCount  int64
	audioFrames int64
	corrupt     int64
	recovery    int64
}

// NewSession builds an empty session.
func NewSession() *Session {
	return &Session{tracks: map[uint32]*trackState{}}
}

func (s *Session) track(id uint32) *trackState {
	ts, ok := s.tracks[id]
	if !ok {
		if len(s.tracks) >= media.MaxTracks {
			return nil
		}
		ts = &trackState{}
		s.tracks[id] = ts
	}
	return ts
}

// Submit validates and schedules one event. Timeline violations are returned
// as media.Error values so they enter the unified error chain rather than
// aborting the run.
func (s *Session) Submit(e Event) error {
	ts := s.track(e.TrackID)
	if ts == nil {
		return media.Errorf(media.CodeLimitExceeded, e.TrackID, nil, -1,
			"track count exceeds %d", media.MaxTracks)
	}
	when := media.Interval{Start: e.PTSNanos, End: e.PTSNanos + e.DurationNanos}
	if e.Incomplete {
		s.corrupt++
		return media.Errorf(media.CodeIncompleteFrame, e.TrackID, &when, -1,
			"seq %d declared without payload", e.Seq)
	}
	if e.DurationNanos <= 0 {
		s.corrupt++
		return media.Errorf(media.CodeNegativeDuration, e.TrackID, &when, -1,
			"seq %d has non-positive duration", e.Seq)
	}
	if ts.haveEvent && e.PTSNanos < ts.lastPTS {
		s.corrupt++
		return media.Errorf(media.CodePTSRollback, e.TrackID, &when, -1,
			"seq %d PTS %d precedes committed %d", e.Seq, e.PTSNanos, ts.lastPTS)
	}

	ts.haveEvent = true
	ts.lastPTS = e.PTSNanos
	ts.lastEnd = when.End
	ts.submitted++
	s.recordMetrics(e)
	s.appendChain(e)
	return nil
}

// CreditAudioFrames adds produced PCM frames (including deterministic silence
// fills) so sample conservation can be verified across recovery.
func (s *Session) CreditAudioFrames(trackID uint32, frames int64) {
	if ts := s.track(trackID); ts != nil {
		ts.samples += frames
		s.audioFrames += frames
	}
}

// CreditRecovery records one recovery action for the summary counters.
func (s *Session) CreditRecovery() { s.recovery++ }

func (s *Session) recordMetrics(e Event) {
	s.latencies = append(s.latencies, e.LatencyNanos)
	if e.HasSyncRef {
		d := e.PTSNanos - e.SyncRefNanos
		if d < 0 {
			d = -d
		}
		s.syncErrors = append(s.syncErrors, d)
	}
}

// appendChain extends the event hash chain: h_i = sha256(h_{i-1} || fp).
func (s *Session) appendChain(e Event) {
	fp := e.Fingerprint
	if fp == nil {
		fp = defaultFingerprint(e)
	}
	buf := make([]byte, 0, len(s.chain)+len(fp))
	buf = append(buf, s.chain[:]...)
	buf = append(buf, fp...)
	s.chain = sha256.Sum256(buf)
	s.chainCount++
}

// EventCount returns the number of committed events in O(1). Unlike Snapshot
// it performs no sorting and is safe to call on the per-packet hot path.
func (s *Session) EventCount() int64 { return s.chainCount }

// TrackSubmitted returns the number of events committed for one track in O(1).
func (s *Session) TrackSubmitted(trackID uint32) int64 {
	if ts, ok := s.tracks[trackID]; ok {
		return ts.submitted
	}
	return 0
}

func defaultFingerprint(e Event) []byte {
	b := make([]byte, 0, 40)
	var u [8]byte
	put := func(v int64) {
		for i := 0; i < 8; i++ {
			u[i] = byte(v >> (56 - 8*i))
		}
		b = append(b, u[:]...)
	}
	b = append(b, byte(e.Kind), byte(e.TrackID))
	put(e.Seq)
	put(e.PTSNanos)
	put(e.DurationNanos)
	return b
}

// Percentile returns the nearest-rank p-th percentile (p in [0,100]) of a
// value slice without mutating the caller's ordering.
func Percentile(values []int64, p float64) int64 {
	if len(values) == 0 {
		return 0
	}
	cp := append([]int64(nil), values...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	rank := int((p/100)*float64(len(cp)) + 0.999999)
	if rank < 1 {
		rank = 1
	}
	if rank > len(cp) {
		rank = len(cp)
	}
	return cp[rank-1]
}

// Metrics is the deterministic measurement snapshot of a session.
type Metrics struct {
	SyncErrorP95Nanos int64
	LatencyP95Nanos   int64
	AudioFrames       int64
	RecoveryCount     int64
	CorruptCount      int64
	EventCount        int64
	EventSHA256       string
}

// Snapshot computes metrics and a checkpoint in one deterministic pass.
func (s *Session) Snapshot() Metrics {
	return Metrics{
		SyncErrorP95Nanos: Percentile(s.syncErrors, 95),
		LatencyP95Nanos:   Percentile(s.latencies, 95),
		AudioFrames:       s.audioFrames,
		RecoveryCount:     s.recovery,
		CorruptCount:      s.corrupt,
		EventCount:        s.chainCount,
		EventSHA256:       hex.EncodeToString(s.chain[:]),
	}
}

// SessionState is the serialisable checkpoint. The hash chain digest and
// counters fully define resumable state; metric slices are rebuilt on resume.
type SessionState struct {
	Tracks      map[uint32]TrackState `json:"tracks"`
	Chain       [32]byte              `json:"chain"`
	ChainCount  int64                 `json:"chain_count"`
	AudioFrames int64                 `json:"audio_frames"`
	Corrupt     int64                 `json:"corrupt"`
	Recovery    int64                 `json:"recovery"`
	SyncErrors  []int64               `json:"sync_errors"`
	Latencies   []int64               `json:"latencies"`
}

// TrackState is the per-track serialisable portion.
type TrackState struct {
	LastPTS   int64 `json:"last_pts"`
	LastEnd   int64 `json:"last_end"`
	HaveEvent bool  `json:"have_event"`
	Samples   int64 `json:"samples"`
	Submitted int64 `json:"submitted"`
}

// Checkpoint serialises the full session for interruption/resume.
func (s *Session) Checkpoint() SessionState {
	st := SessionState{
		Chain: s.chain, ChainCount: s.chainCount, AudioFrames: s.audioFrames,
		Corrupt: s.corrupt, Recovery: s.recovery,
		SyncErrors: append([]int64(nil), s.syncErrors...),
		Latencies:  append([]int64(nil), s.latencies...),
		Tracks:     map[uint32]TrackState{},
	}
	for id, ts := range s.tracks {
		st.Tracks[id] = TrackState{
			LastPTS: ts.lastPTS, LastEnd: ts.lastEnd, HaveEvent: ts.haveEvent,
			Samples: ts.samples, Submitted: ts.submitted,
		}
	}
	return st
}

// RestoreSession rebuilds a session from a checkpoint.
func RestoreSession(st SessionState) *Session {
	s := NewSession()
	s.chain, s.chainCount = st.Chain, st.ChainCount
	s.audioFrames, s.corrupt, s.recovery = st.AudioFrames, st.Corrupt, st.Recovery
	s.syncErrors = append([]int64(nil), st.SyncErrors...)
	s.latencies = append([]int64(nil), st.Latencies...)
	for id, t := range st.Tracks {
		s.tracks[id] = &trackState{
			lastPTS: t.LastPTS, lastEnd: t.LastEnd, haveEvent: t.HaveEvent,
			samples: t.Samples, submitted: t.Submitted,
		}
	}
	return s
}

// VerifyContinuity asserts that audio frame credits form one unbroken run
// across a recovery boundary: framesBefore plus filled frames must equal the
// total credited frames with no gap or double count.
func (s *Session) VerifyContinuity(framesBefore, framesAfter, filled int64) error {
	if framesBefore+filled != framesAfter {
		return media.Errorf(media.CodeCheckpointMismatch, 0, nil, -1,
			"audio frames discontinuous: before=%d filled=%d after=%d",
			framesBefore, filled, framesAfter)
	}
	return nil
}
