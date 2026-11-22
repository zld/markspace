package rtprecover

import (
	"errors"
	"testing"

	"github.com/pion/rtp"

	"mediasync/internal/clock"
	"mediasync/internal/testmedia"
)

func newTimeline(t *testing.T, track uint32, rate int64) *clock.Timeline {
	t.Helper()
	tl := clock.NewTimeline()
	if err := tl.Add(clock.TrackClock{Track: track, TicksPerSecond: rate}); err != nil {
		t.Fatal(err)
	}
	return tl
}

func packet(seq uint16, ts uint32) *rtp.Packet {
	return &rtp.Packet{
		Header:  rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: ts, SSRC: 1},
		Payload: []byte{byte(seq >> 8), byte(seq)},
	}
}

func TestInOrderEmission(t *testing.T) {
	r, err := NewRecoverer(1, 4, newTimeline(t, 1, 90000))
	if err != nil {
		t.Fatal(err)
	}
	var emitted []uint16
	for seq := uint16(0); seq < 6; seq++ {
		got, err := r.Push(packet(seq, uint32(seq)*3000))
		if err != nil {
			t.Fatal(err)
		}
		// Low-latency mode: each contiguous packet emits immediately.
		if len(got) != 1 || got[0].Packet.SequenceNumber != seq {
			t.Fatalf("seq %d emitted %v", seq, got)
		}
		emitted = append(emitted, got[0].Packet.SequenceNumber)
	}
	rest, err := r.Flush()
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 0 { // everything already drained
		t.Fatalf("flush emitted %d unexpected packets", len(rest))
	}
	if len(emitted) != 6 {
		t.Fatalf("total emitted %d", len(emitted))
	}
}

func TestReorderingWithinWindow(t *testing.T) {
	r, _ := NewRecoverer(1, 8, newTimeline(t, 1, 90000))
	order := []uint16{0, 2, 1, 3, 4, 5, 6, 7, 8}
	var emitted []uint16
	for _, seq := range order {
		got, err := r.Push(packet(seq, uint32(seq)*3000))
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range got {
			emitted = append(emitted, d.Packet.SequenceNumber)
		}
	}
	rest, err := r.Flush()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range rest {
		emitted = append(emitted, d.Packet.SequenceNumber)
	}
	for i, seq := range emitted {
		if seq != uint16(i) {
			t.Fatalf("emission not sorted: %v", emitted)
		}
	}
	st := r.Stats()
	if st.Recovered == 0 || st.Reordered == 0 || st.Unrecoverable != 0 {
		t.Fatalf("recovery stats wrong: %+v", st)
	}
}

func TestDuplicatesCounted(t *testing.T) {
	r, _ := NewRecoverer(1, 8, newTimeline(t, 1, 90000))
	pkts := []*rtp.Packet{packet(0, 0), packet(0, 0), packet(1, 3000), packet(0, 0)}
	for _, p := range pkts {
		if _, err := r.Push(p); err != nil {
			t.Fatal(err)
		}
	}
	if st := r.Stats(); st.Duplicates != 2 || st.Received != 2 {
		t.Fatalf("duplicate accounting: %+v", st)
	}
}

func TestWindowLossRecoveredThenAged(t *testing.T) {
	// Window 4: seq 1 missing. Packets 2..5 arrive in time to keep the gap
	// inside the window, and seq 1 then arrives late: recovered, no loss.
	r, _ := NewRecoverer(1, 4, newTimeline(t, 1, 90000))
	for _, seq := range []uint16{0, 2, 3, 4, 5, 1} {
		_, err := r.Push(packet(seq, uint32(seq)*3000))
		if err != nil {
			t.Fatalf("in-window recovery failed at seq %d: %v", seq, err)
		}
	}
	if st := r.Stats(); st.Unrecoverable != 0 || st.Recovered != 1 {
		t.Fatalf("want 1 recovered, 0 lost: %+v", st)
	}
}

func TestOutOfWindowLossIsUnrecoverable(t *testing.T) {
	// Window 2: seq 1 never arrives. By the time seq 4 is pushed, the gap
	// has aged out and must surface as KindPacketLoss.
	r, _ := NewRecoverer(1, 2, newTimeline(t, 1, 90000))
	var sawLoss bool
	for _, seq := range []uint16{0, 2, 3, 4} {
		_, err := r.Push(packet(seq, uint32(seq)*3000))
		if err != nil {
			var ce *clock.Error
			if errors.As(err, &ce) && ce.Kind == clock.KindPacketLoss {
				sawLoss = true
			} else {
				t.Fatalf("unexpected error: %v", err)
			}
		}
	}
	if !sawLoss {
		t.Fatal("aged-out gap must propagate KindPacketLoss")
	}
	st := r.Stats()
	if st.Unrecoverable != 1 {
		t.Fatalf("unrecoverable = %d want 1", st.Unrecoverable)
	}
	if rate := st.RecoveryRate(); rate >= 1.0 || rate <= 0.5 {
		t.Fatalf("recovery rate implausible: %f", rate)
	}
}

func TestStreamStatsMeetDeliveryTarget(t *testing.T) {
	// 2% loss, modest jitter: a window of 32 must keep delivery >= 0.95.
	stream := testmedia.BuildRTPStream(testmedia.RTPStreamSpec{
		Seed: 41, SSRC: 7, Packets: 500, ClockRate: 48000, FrameStep: 960,
		LossRate: 0.02, ReorderRate: 0.1, DuplicateRate: 0.05,
	})
	r, err := NewRecoverer(7, 32, newTimeline(t, 7, 48000))
	if err != nil {
		t.Fatal(err)
	}
	var lossErrs int
	for _, pkt := range stream.Packets {
		_, err := r.Push(pkt)
		if err != nil {
			var ce *clock.Error
			if errors.As(err, &ce) && ce.Kind == clock.KindPacketLoss {
				lossErrs++
			}
		}
	}
	if _, err := r.Flush(); err != nil {
		var ce *clock.Error
		if !errors.As(err, &ce) || ce.Kind != clock.KindPacketLoss {
			t.Fatalf("flush: %v", err)
		}
		lossErrs++
	}
	st := r.Stats()
	if st.Unrecoverable != len(stream.Lost) {
		t.Fatalf("lost=%d accounted=%d", len(stream.Lost), st.Unrecoverable)
	}
	if st.RecoveryRate() < 0.95 {
		t.Fatalf("recovery rate %.4f below 0.95", st.RecoveryRate())
	}
	if st.Duplicates == 0 {
		t.Fatal("impairment generator produced no duplicates")
	}
	t.Logf("stats: %+v rate=%.4f lossErrs=%d", st, st.RecoveryRate(), lossErrs)
}

func TestWindowBoundsRejected(t *testing.T) {
	if _, err := NewRecoverer(1, 0, newTimeline(t, 1, 1)); err == nil {
		t.Fatal("zero window must fail")
	}
	if _, err := NewRecoverer(1, MaxRecoveryWindow+1, newTimeline(t, 1, 1)); err == nil {
		t.Fatal("oversize window must fail")
	}
}
