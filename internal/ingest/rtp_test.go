package ingest

import (
	"errors"
	"testing"

	"github.com/pion/rtp"

	"regress-kernel/internal/media"
)

func recvFromFixture(t *testing.T, fx *media.Fixture) *RTPReceiver {
	t.Helper()
	r := NewRTPReceiver(DefaultRTPConfig())
	for _, p := range fx.Delivered {
		if err := r.Receive(p); err != nil {
			t.Fatalf("receive: %v", err)
		}
	}
	return r
}

func TestRTPReordersAndRecoversIsolatedLoss(t *testing.T) {
	fx := media.Generate(media.DefaultConfig("normal", 17))
	r := recvFromFixture(t, fx)
	out, rep, err := r.Finalize()
	if err != nil {
		t.Fatalf("normal fixture must finalize cleanly: %v", err)
	}
	// Output is gap-free and strictly sequence ordered.
	for i := 1; i < len(out); i++ {
		if out[i].Seq != out[i-1].Seq+1 {
			t.Fatalf("reordered stream not contiguous at %d: %d -> %d",
				i, out[i-1].Seq, out[i].Seq)
		}
	}
	if len(out) != fx.Config.AudioPackets {
		t.Fatalf("got %d packets, want full %d after PLC",
			len(out), fx.Config.AudioPackets)
	}
	if rep.Injected != len(fx.LostSeqs) || rep.Recovered != rep.Injected {
		t.Fatalf("all isolated loss must recover: %+v", rep)
	}
	if rep.RecoveryRate < 0.95 || rep.RecoveryRate > 1.0 {
		t.Fatalf("recovery rate %v outside [0.95,1]", rep.RecoveryRate)
	}
	// Recovered packets keep the extrapolated 20ms timestamp cadence.
	for _, ap := range out {
		if ap.Recovered && ap.RTPTimestamp%fx.Config.AudioFrameTicks !=
			fx.Config.AudioOffsetTicks%fx.Config.AudioFrameTicks {
			t.Fatalf("recovered packet %d has broken cadence: %d", ap.Seq, ap.RTPTimestamp)
		}
	}
}

func TestRTPUnrecoverableBurst(t *testing.T) {
	fx := media.GenerateUnrecoverable(media.DefaultConfig("normal", 17))
	r := recvFromFixture(t, fx)
	out, rep, err := r.Finalize()
	if !errors.Is(err, media.ErrRTPLoss) {
		t.Fatalf("burst loss must wrap ErrRTPLoss, got %v", err)
	}
	if rep.Unrecoverable != 3 || rep.Recovered != 0 {
		t.Fatalf("loss accounting wrong: %+v", rep)
	}
	var flagged int
	for _, ap := range out {
		if ap.Unrecoverable {
			flagged++
		}
	}
	if flagged != 3 {
		t.Fatalf("want 3 explicitly flagged packets, got %d", flagged)
	}
	// Nothing was silently skipped: the timeline stays contiguous.
	if len(out) != fx.Config.AudioPackets {
		t.Fatalf("stream length %d want %d", len(out), fx.Config.AudioPackets)
	}
}

func TestRTPReorderWindowEnforced(t *testing.T) {
	cfg := media.DefaultConfig("normal", 17)
	cfg.ReorderSwaps = 0
	cfg.LossRate = 0
	fx := media.Generate(cfg)
	// Move the second packet four positions late, beyond tolerance 3:
	// delivery p0,p2,p3,p4,p5,p1,... gives p1 arrival rank 5 vs sorted rank 1.
	delivered := make([]*rtp.Packet, len(fx.Delivered))
	copy(delivered, fx.Delivered)
	late := delivered[1]
	delivered = append(delivered[:1], fx.Delivered[2:6]...)
	delivered = append(delivered, late)
	delivered = append(delivered, fx.Delivered[6:]...)
	r := NewRTPReceiver(DefaultRTPConfig())
	for _, p := range delivered {
		if err := r.Receive(p); err != nil {
			t.Fatalf("receive: %v", err)
		}
	}
	if _, _, err := r.Finalize(); !errors.Is(err, media.ErrRTPOrder) {
		t.Fatalf("want ErrRTPOrder for late packet, got %v", err)
	}
}

func TestRTPDuplicatesAndCap(t *testing.T) {
	fx := media.Generate(media.DefaultConfig("normal", 17))
	r := NewRTPReceiver(DefaultRTPConfig())
	for _, p := range fx.Delivered {
		if err := r.Receive(p); err != nil {
			t.Fatalf("receive: %v", err)
		}
		if err := r.Receive(p); err != nil { // redelivery
			t.Fatalf("duplicate must not error, got %v", err)
		}
	}
	if r.Duplicates() != len(fx.Delivered) {
		t.Fatalf("duplicate count %d", r.Duplicates())
	}
	out, _, err := r.Finalize()
	if err != nil || len(out) != fx.Config.AudioPackets {
		t.Fatalf("duplicates must not change the stream: len=%d err=%v", len(out), err)
	}

	cfg := DefaultRTPConfig()
	cfg.MaxPackets = 3
	small := NewRTPReceiver(cfg)
	for i, p := range fx.Delivered {
		if i >= 4 {
			break
		}
		err := small.Receive(p)
		if i == 3 && !errors.Is(err, media.ErrLimitExceeded) {
			t.Fatalf("packet cap: want ErrLimitExceeded, got %v", err)
		}
	}
}

func TestRTPSequenceWrap(t *testing.T) {
	r := NewRTPReceiver(DefaultRTPConfig())
	// Sequences cross the uint16 boundary with one loss (65535).
	for _, seq := range []uint16{65534, 0, 1} {
		if err := r.Receive(&rtp.Packet{
			Header:  rtp.Header{SequenceNumber: seq, Timestamp: uint32(seq) * 960},
			Payload: make([]byte, 4),
		}); err != nil {
			t.Fatalf("receive: %v", err)
		}
	}
	out, rep, err := r.Finalize()
	if err != nil {
		t.Fatalf("wrap loss should be recoverable: %v", err)
	}
	want := []uint16{65534, 65535, 0, 1}
	if len(out) != len(want) {
		t.Fatalf("got %d want %d packets", len(out), len(want))
	}
	for i := range want {
		if out[i].Seq != want[i] {
			t.Fatalf("position %d: got %d want %d", i, out[i].Seq, want[i])
		}
	}
	if rep.Recovered != 1 || !out[1].Recovered {
		t.Fatalf("wrapped loss not recovered: %+v", rep)
	}
}
