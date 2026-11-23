package rtp

import (
	"math"
	"testing"

	"github.com/pion/rtp"

	"mediaqc/internal/adapter"
	"mediaqc/internal/clock"
	"mediaqc/internal/model"
)

func datagram(seq uint16, ts uint32, payload byte) []byte {
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           4242,
		},
		Payload: []byte{payload},
	}
	b, err := pkt.Marshal()
	if err != nil {
		panic(err)
	}
	return b
}

func TestParsePacketAndUnwrap(t *testing.T) {
	p := NewParser(adapter.DefaultLimits(), 90000)
	seqWrap, _ := clock.NewWraparound(16)
	tsWrap, _ := clock.NewWraparound(32)

	ev, err := p.ParsePacket("mat-rtp", "v", model.TrackVideo, datagram(65535, math.MaxUint32-1, 7), seqWrap, tsWrap)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Sequence != 65535 || ev.Start.Ticks() != int64(math.MaxUint32)-1 {
		t.Fatalf("first packet wrong: %+v", ev)
	}
	ev2, err := p.ParsePacket("mat-rtp", "v", model.TrackVideo, datagram(1, 2, 8), seqWrap, tsWrap)
	if err != nil {
		t.Fatal(err)
	}
	if ev2.Sequence != 65537 {
		t.Fatalf("sequence wrap = %d, want 65537", ev2.Sequence)
	}
	// timestamp wrap: MaxUint32-1 -> 2 advances by 4 across the boundary
	if ev2.Start.Ticks() != int64(math.MaxUint32)+3 {
		t.Fatalf("timestamp wrap = %d, want %d", ev2.Start.Ticks(), int64(math.MaxUint32)+3)
	}
}

func TestParseStreamGapsAndDuplicates(t *testing.T) {
	p := NewParser(adapter.DefaultLimits(), 90000)
	dgs := [][]byte{
		datagram(0, 0, 1),
		datagram(1, 3000, 2),
		// seq 2 missing
		datagram(3, 9000, 4),
		datagram(1, 3000, 2), // duplicate
	}
	events, gaps, errs := p.ParseStream("mat-rtp2", "v", model.TrackVideo, dgs)
	if len(events) != 3 {
		t.Fatalf("events=%d, want 3 (duplicate dropped)", len(events))
	}
	if len(gaps) != 1 || gaps[0].FirstSeq != 2 || gaps[0].LastSeq != 2 {
		t.Fatalf("gap detection wrong: %+v", gaps)
	}
	if len(errs) != 1 || !model.HasCode(errs[0], model.CodeDuplicateSeq) {
		t.Fatalf("want one duplicate error, got %+v", errs)
	}
}

func TestTruncatedAndOversizedRejected(t *testing.T) {
	p := NewParser(adapter.DefaultLimits(), 90000)
	seqWrap, _ := clock.NewWraparound(16)
	tsWrap, _ := clock.NewWraparound(32)
	if _, err := p.ParsePacket("mat-bad", "v", model.TrackVideo, []byte{0x80, 0x60}, seqWrap, tsWrap); err == nil {
		t.Fatal("truncated datagram must be rejected")
	}
	lim := adapter.DefaultLimits()
	lim.MaxBytes = 4
	small := NewParser(lim, 90000)
	if _, err := small.ParsePacket("mat-big", "v", model.TrackVideo,
		datagram(0, 0, 1), seqWrap, tsWrap); err == nil {
		t.Fatal("oversized datagram must be rejected")
	}
}

func TestZeroClockRateRejected(t *testing.T) {
	p := NewParser(adapter.DefaultLimits(), 0)
	seqWrap, _ := clock.NewWraparound(16)
	tsWrap, _ := clock.NewWraparound(32)
	_, err := p.ParsePacket("mat-zero", "v", model.TrackVideo, datagram(0, 0, 1), seqWrap, tsWrap)
	if err == nil || !model.HasCode(err, model.CodeZeroTimescale) {
		t.Fatalf("want zero-timescale error, got %v", err)
	}
}

func TestRegistryRegistration(t *testing.T) {
	got, err := adapter.Default.Lookup(adapter.KindRTP, "rtp-local", "1.8.11")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Supports("fec-xor") {
		t.Fatal("rtp parser must advertise fec-xor")
	}
}
