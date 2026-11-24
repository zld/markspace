package report

import (
	"testing"

	"mediacheck/internal/bitstream"
	"mediacheck/internal/damage"
	"mediacheck/internal/media"
	rtppkg "mediacheck/internal/rtp"
	"mediacheck/internal/sync"
)

func line(t *testing.T, id uint32, kind media.SampleKind, n int) *media.TrackLine {
	t.Helper()
	tb, _ := media.NewTimeBase(1000)
	raw := make([]media.RawSample, n)
	for i := range raw {
		raw[i] = media.RawSample{MediaTick: int64(i) * 40, DurationTick: 40,
			IsSync: kind == media.KindVideo && i%10 == 0, Source: media.SourceDecoded}
	}
	l, err := media.BuildTrackLine(id, kind, tb, raw, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestBuildAggregatesAllSubsystems(t *testing.T) {
	v := line(t, 1, media.KindVideo, 100)
	a := line(t, 2, media.KindAudio, 100)
	kf := bitstream.Analyze(v)
	damages := []damage.Error{
		{Code: damage.CodeTruncatedBox, TrackID: 1,
			Range:       damage.Range{StartNanos: 1, EndNanos: 2, Recoverable: damage.RecoverableNo},
			Recoverable: damage.RecoverableNo},
	}
	rtpRes := &rtppkg.Result{Stats: rtppkg.Stats{Recovered: 18, Lost: 20, LossRecoveryRate: 0.9}}
	sr := &sync.Report{AbsMeanMs: 12.5}
	lat := make([]int64, 100)
	for i := range lat {
		lat[i] = int64(i+1) * 1_000_000 // 1..100 ms
	}
	m := Build(Input{
		Lines: []*media.TrackLine{v, a}, Keyframes: []bitstream.Report{kf},
		Damages: damages, RTP: rtpRes, Sync: sr, Latencies: lat,
	})
	if len(m.Tracks) != 2 || m.Tracks[0].TrackID != 1 {
		t.Fatalf("tracks must be sorted by id, got %+v", m.Tracks)
	}
	if m.RecoveredPackets != 18 || m.LossRecoveryRate != 0.9 {
		t.Fatal("RTP metrics must propagate")
	}
	if m.AVSyncErrorMs != 12.5 {
		t.Fatalf("av sync = %f, want 12.5", m.AVSyncErrorMs)
	}
	if !m.HasUnrecoverable {
		t.Fatal("a no-recover damage must set HasUnrecoverable for exit code 3")
	}
	if m.P95LatencyMs != 95 {
		t.Errorf("p95 latency = %d, want 95 ms", m.P95LatencyMs)
	}
	if m.KeyframeGapMs[1] == 0 {
		t.Error("keyframe gap map must be keyed by track id")
	}
	if m.Tracks[0].Keyframes == 0 {
		t.Error("keyframe count must be stamped onto the track summary")
	}
}

func TestBuildDeterministicOrdering(t *testing.T) {
	// Same damage set supplied in two orders must normalize identically.
	d1 := []damage.Error{
		{Code: damage.CodeMissingSample, TrackID: 2, Range: damage.Range{StartNanos: 9}},
		{Code: damage.CodeTruncatedBox, TrackID: 1, Range: damage.Range{StartNanos: 1}},
	}
	d2 := []damage.Error{d1[1], d1[0]}
	m1 := Build(Input{Damages: d1})
	m2 := Build(Input{Damages: d2})
	if len(m1.DamageErrors) != len(m2.DamageErrors) {
		t.Fatal("normalized damage length mismatch")
	}
	for i := range m1.DamageErrors {
		if m1.DamageErrors[i].Code != m2.DamageErrors[i].Code ||
			m1.DamageErrors[i].TrackID != m2.DamageErrors[i].TrackID {
			t.Fatal("damage errors must be deterministically ordered")
		}
	}
}

func TestBuildEmptyLatencyIsZero(t *testing.T) {
	m := Build(Input{})
	if m.P95LatencyMs != 0 || m.LossRecoveryRate != 0 || m.HasUnrecoverable {
		t.Fatalf("empty input must yield zero-valued metrics, got %+v", m)
	}
}
