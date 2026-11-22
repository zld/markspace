package pipeline

import (
	"testing"

	"mediacore/media"
)

func audioEvent(seq, pts int64, latency int64, ref int64) Event {
	return Event{
		TrackID: 1, Kind: EventAudio, Seq: seq, PTSNanos: pts,
		DurationNanos: 10_000_000, LatencyNanos: latency,
		SyncRefNanos: ref, HasSyncRef: true,
	}
}

func TestSessionMonotonicAndInvariants(t *testing.T) {
	s := NewSession()
	for i := int64(0); i < 5; i++ {
		if err := s.Submit(audioEvent(i, i*10_000_000, 5_000_000, i*10_000_000)); err != nil {
			t.Fatal(err)
		}
	}
	// PTS rollback is a located error, not a panic
	err := s.Submit(audioEvent(6, 10_000_000, 0, 0))
	if me, ok := media.AsError(err); !ok || me.Code != media.CodePTSRollback {
		t.Fatalf("want pts_rollback, got %v", err)
	}
	// negative duration and incomplete frame enter the same error chain
	bad := Event{TrackID: 1, Kind: EventVideo, Seq: 7, PTSNanos: 60_000_000, DurationNanos: 0}
	if me, ok := media.AsError(s.Submit(bad)); !ok || me.Code != media.CodeNegativeDuration {
		t.Fatal("negative duration not rejected")
	}
	inc := Event{TrackID: 1, Kind: EventVideo, Seq: 8, PTSNanos: 70_000_000, DurationNanos: 1000, Incomplete: true}
	if me, ok := media.AsError(s.Submit(inc)); !ok || me.Code != media.CodeIncompleteFrame {
		t.Fatal("incomplete frame not rejected")
	}
	// the rejected events were not committed, so the hash chain has 5 events
	if s.Snapshot().EventCount != 5 {
		t.Fatal("rejected event was committed")
	}
}

func TestSessionDeterministicHashChain(t *testing.T) {
	build := func() *Session {
		s := NewSession()
		for i := int64(0); i < 20; i++ {
			s.Submit(audioEvent(i, i*10_000_000, 3_000_000, i*10_000_000))
		}
		return s
	}
	h1 := build().Snapshot().EventSHA256
	h2 := build().Snapshot().EventSHA256
	if h1 == "" || h1 != h2 {
		t.Fatalf("event hash not deterministic: %s vs %s", h1, h2)
	}
}

func TestSessionPercentiles(t *testing.T) {
	vals := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if got := Percentile(vals, 95); got != 10 {
		t.Fatalf("p95=%d want 10", got)
	}
	if got := Percentile(vals, 50); got != 5 {
		t.Fatalf("p50=%d want 5", got)
	}
	if got := Percentile(nil, 95); got != 0 {
		t.Fatal("empty percentile must be zero")
	}
	// caller's slice ordering must be preserved
	orig := append([]int64(nil), vals...)
	Percentile(vals, 95)
	for i := range vals {
		if vals[i] != orig[i] {
			t.Fatal("percentile mutated input ordering")
		}
	}
}

func TestSessionCheckpointResumeAndContinuity(t *testing.T) {
	s := NewSession()
	for i := int64(0); i < 10; i++ {
		s.Submit(audioEvent(i, i*10_000_000, 2_000_000, i*10_000_000))
		s.CreditAudioFrames(1, 480)
	}
	before := s.Snapshot()
	st := s.Checkpoint()

	restored := RestoreSession(st)
	if restored.Snapshot().EventSHA256 != before.EventSHA256 {
		t.Fatal("hash chain diverges after restore")
	}
	// continue the stream; new events extend the same chain
	for i := int64(10); i < 15; i++ {
		restored.Submit(audioEvent(i, i*10_000_000, 2_000_000, i*10_000_000))
		restored.CreditAudioFrames(1, 480)
	}
	// a fresh run over the identical full sequence must match
	full := NewSession()
	for i := int64(0); i < 15; i++ {
		full.Submit(audioEvent(i, i*10_000_000, 2_000_000, i*10_000_000))
		full.CreditAudioFrames(1, 480)
	}
	if restored.Snapshot().EventSHA256 != full.Snapshot().EventSHA256 {
		t.Fatal("resumed session hash differs from uninterrupted run")
	}

	// sample conservation across a recovery boundary
	framesBefore := int64(10 * 480)
	filled := int64(3 * 480)
	if err := restored.VerifyContinuity(framesBefore, framesBefore+filled, filled); err != nil {
		t.Fatal(err)
	}
	if err := restored.VerifyContinuity(framesBefore, framesBefore, filled); err == nil {
		t.Fatal("discontinuous frame counts were accepted")
	}
}

func TestSessionOOneCounters(t *testing.T) {
	s := NewSession()
	for i := int64(0); i < 100; i++ {
		if err := s.Submit(audioEvent(i, i*10_000_000, 1_000_000, i*10_000_000)); err != nil {
			t.Fatal(err)
		}
	}
	if s.EventCount() != 100 {
		t.Fatalf("EventCount=%d", s.EventCount())
	}
	if s.TrackSubmitted(1) != 100 || s.TrackSubmitted(99) != 0 {
		t.Fatal("per-track submitted count wrong")
	}
	// the cheap accessor must equal the snapshot's own count without sorting
	if s.EventCount() != s.Snapshot().EventCount {
		t.Fatal("O(1) counter disagrees with snapshot")
	}
}

func TestSessionMetricsAggregation(t *testing.T) {
	s := NewSession()
	for i := int64(0); i < 100; i++ {
		lat := int64(1_000_000 + (i%5)*10_000_000) // 1..41 ms
		s.Submit(Event{
			TrackID: 1, Kind: EventAudio, Seq: i, PTSNanos: i * 10_000_000,
			DurationNanos: 10_000_000, LatencyNanos: lat,
			SyncRefNanos: i*10_000_000 + (i%3)*2_000_000, HasSyncRef: true,
		})
	}
	m := s.Snapshot()
	if m.LatencyP95Nanos <= 0 || m.SyncErrorP95Nanos < 0 {
		t.Fatalf("bad metrics: %+v", m)
	}
	if m.LatencyP95Nanos > 50_000_000 {
		t.Fatalf("latency p95 %d exceeds 50ms", m.LatencyP95Nanos)
	}
}
