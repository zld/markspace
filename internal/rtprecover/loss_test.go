package rtprecover

import (
	"testing"

	"github.com/medialab/medialab/internal/clock"
	"github.com/pion/rtp"
)

// linearTick maps seq n to tick n*100, giving gap ranges deterministic times.
func linearTick(seq uint16) int64 { return int64(seq) * 100 }

func videoPacket(t *testing.T, seq uint16, keyframe bool) *rtp.Packet {
	t.Helper()
	flags := FlagVideo
	if keyframe {
		flags |= FlagKeyframe
	}
	env := Envelope{Flags: flags, Body: []byte("b-")}
	return &rtp.Packet{
		Header:  rtp.Header{Version: 2, SequenceNumber: seq, SSRC: 7, Timestamp: uint32(seq)},
		Payload: env.Marshal(),
	}
}

func pushAll(t *testing.T, r *Recover, pkts []*rtp.Packet) {
	t.Helper()
	for _, p := range pkts {
		if err := r.Push(p); err != nil {
			t.Fatalf("push seq %d: %v", p.SequenceNumber, err)
		}
	}
}

func TestEnvelopeRoundTripAndChecksum(t *testing.T) {
	env := Envelope{Flags: FlagKeyframe | FlagVideo, Body: []byte{9, 8, 7}}
	raw := env.Marshal()
	got, err := ParseEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Flags != env.Flags || string(got.Body) != string(env.Body) {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	raw[1] ^= 0xFF
	if _, err := ParseEnvelope(raw); err == nil {
		t.Fatal("corrupted payload must fail CRC")
	} else if me, ok := clock.As(err); !ok || me.Kind != clock.KindChecksum {
		t.Fatalf("want checksum error, got %v", err)
	}
	if _, err := ParseEnvelope([]byte{0, 1}); err == nil {
		t.Fatal("truncated envelope must fail")
	}
}

func TestChecksumBadNeverEntersWindow(t *testing.T) {
	r := NewRecover(RecoverConfig{WindowCapacity: 8, BurstThreshold: 1, TrackID: "v0", IsVideo: true}, linearTick)
	good := videoPacket(t, 0, true)
	bad := videoPacket(t, 1, false)
	bad.Payload[1] ^= 0xFF
	if err := r.Push(good); err != nil {
		t.Fatal(err)
	}
	if err := r.Push(bad); err == nil {
		t.Fatal("checksum-bad packet must be rejected")
	}
	if r.Stats().ChecksumBad != 1 {
		t.Fatalf("checksumBad = %d, want 1", r.Stats().ChecksumBad)
	}
	// Plausible packets drain normally; only the bad one is withheld.
	drained, err := r.Drain()
	if err != nil {
		t.Fatal(err)
	}
	if len(drained) != 1 || drained[0].Seq != 0 {
		t.Fatalf("drained = %+v, want only seq 0", drained)
	}
	// End-of-stream confirms the missing seq 1 as a gap; no sample is
	// fabricated for it.
	tail, err := r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) != 0 {
		t.Fatalf("close samples = %+v, want none (seq 1 was lost)", tail)
	}
	if gaps := r.Gaps(); len(gaps) != 1 || gaps[0].StartSeq != 1 || gaps[0].EndSeq != 1 {
		t.Fatalf("gaps = %+v", gaps)
	}
}

func TestBurstThresholdAndGapTimeRange(t *testing.T) {
	r := NewRecover(RecoverConfig{WindowCapacity: 16, BurstThreshold: 3, TrackID: "v0", IsVideo: true}, linearTick)
	// Missing seq 1..3 (a 3-packet burst), present 0,4,5.
	pushAll(t, r, []*rtp.Packet{videoPacket(t, 0, true), videoPacket(t, 4, true), videoPacket(t, 5, false)})
	samples, err := r.Drain()
	if err != nil {
		t.Fatal(err)
	}
	gaps := r.Gaps()
	if len(gaps) != 1 {
		t.Fatalf("gaps = %+v, want exactly one", gaps)
	}
	g := gaps[0]
	if g.StartSeq != 1 || g.EndSeq != 3 || g.Length != 3 {
		t.Fatalf("gap span = %d..%d len %d", g.StartSeq, g.EndSeq, g.Length)
	}
	if g.Ticks.Start != 100 || g.Ticks.End != 401 {
		t.Fatalf("gap time range = %+v", g.Ticks)
	}
	// Emitted seqs are exactly the real packets 0,4,5 — nothing fabricated.
	want := []uint16{0, 4, 5}
	if len(samples) != len(want) {
		t.Fatalf("samples = %+v, want %v", samples, want)
	}
	for i, s := range samples {
		if s.Seq != want[i] {
			t.Fatalf("sample[%d] seq = %d, want %d", i, s.Seq, want[i])
		}
	}
}

func TestSubthresholdHoleWaitsForReorder(t *testing.T) {
	r := NewRecover(RecoverConfig{WindowCapacity: 8, BurstThreshold: 3, TrackID: "v0", IsVideo: true}, linearTick)
	pushAll(t, r, []*rtp.Packet{videoPacket(t, 0, true), videoPacket(t, 3, false)})
	if _, err := r.Drain(); err != nil {
		t.Fatal(err)
	}
	if len(r.Gaps()) != 0 {
		t.Fatalf("small reorder hole must not be a gap yet: %+v", r.Gaps())
	}
	// Reordered packets fill the hole before threshold time.
	pushAll(t, r, []*rtp.Packet{videoPacket(t, 1, false), videoPacket(t, 2, false)})
	samples, err := r.Drain()
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Gaps()) != 0 {
		t.Fatalf("filled hole must never become a gap: %+v", r.Gaps())
	}
	if len(samples) != 3 || samples[0].Seq != 1 || samples[2].Seq != 3 {
		t.Fatalf("samples = %+v", samples)
	}
}

func TestVideoWaitsForKeyframe(t *testing.T) {
	r := NewRecover(RecoverConfig{WindowCapacity: 16, BurstThreshold: 2, TrackID: "v0", IsVideo: true}, linearTick)
	// seq 1,2 lost; seq 3 arrives as a delta frame, seq 4 as a keyframe.
	pushAll(t, r, []*rtp.Packet{
		videoPacket(t, 0, true),
		videoPacket(t, 3, false),
		videoPacket(t, 4, true),
	})
	samples, err := r.Drain()
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Gaps()) != 1 {
		t.Fatalf("gaps = %+v", r.Gaps())
	}
	var delta, key *Sample
	for i := range samples {
		switch samples[i].Seq {
		case 3:
			delta = &samples[i]
		case 4:
			key = &samples[i]
		}
	}
	if delta == nil || key == nil {
		t.Fatalf("missing samples: %+v", samples)
	}
	if !delta.WaitingKF {
		t.Fatal("delta after gap must be flagged WaitingKF")
	}
	if key.WaitingKF {
		t.Fatal("keyframe clears the waiting state")
	}
	if !key.Keyframe {
		t.Fatal("seq 4 must be a keyframe")
	}
}

func TestAudioResumesWithoutKeyframeWait(t *testing.T) {
	tick := linearTick
	r := NewRecover(RecoverConfig{WindowCapacity: 8, BurstThreshold: 2, TrackID: "a0", IsVideo: false}, tick)
	env := Envelope{Flags: FlagAudio, Body: []byte("a")}
	pkts := []*rtp.Packet{
		{Header: rtp.Header{SequenceNumber: 0}, Payload: env.Marshal()},
		{Header: rtp.Header{SequenceNumber: 3}, Payload: env.Marshal()},
	}
	pushAll(t, r, pkts)
	samples, err := r.Drain()
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Gaps()) != 1 {
		t.Fatalf("gaps = %+v", r.Gaps())
	}
	if len(samples) != 2 || samples[1].WaitingKF {
		t.Fatalf("audio must not wait for a keyframe: %+v", samples)
	}
}

func TestWindowStatsMerged(t *testing.T) {
	r := NewRecover(RecoverConfig{WindowCapacity: 4, BurstThreshold: 1, TrackID: "v0", IsVideo: true}, linearTick)
	_ = r.Push(videoPacket(t, 0, true))
	if err := r.Push(videoPacket(t, 0, true)); err == nil {
		t.Fatal("duplicate must error")
	}
	st := r.Stats()
	if st.Duplicates != 1 || st.Received != 2 {
		t.Fatalf("stats = %+v", st)
	}
}
