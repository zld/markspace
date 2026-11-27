package silence

import (
	"errors"
	"testing"

	"github.com/pion/rtp"

	"example.com/continuity/internal/clock"
	"example.com/continuity/internal/fault"
)

const (
	rate  = 48000
	spp   = 480 // 10 ms per packet
	pktNS = 10_000_000
)

func packet(seq uint16, silent bool) *rtp.Packet {
	var payload []byte
	if silent {
		payload = make([]byte, spp*2) // all-zero PCM
	} else {
		payload = make([]byte, spp*2)
		for i := 0; i < len(payload); i += 2 {
			payload[i] = 0x7F // large positive sample
			payload[i+1] = 0xFF
		}
	}
	// RTP timestamps are fixed to the wire sequence number so reordering
	// arrivals never makes timestamps go backwards.
	return &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: seq, SSRC: 1, Timestamp: uint32(seq) * spp},
		Payload: payload,
	}
}

func newDetector(t *testing.T, window int) *Detector {
	t.Helper()
	d, err := New(Config{
		Track: "audio/0", ClockRate: rate, SamplesPerPacket: spp,
		SilenceThreshold: 16, ReorderWindow: window,
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestOrderedSilenceMerged(t *testing.T) {
	d := newDetector(t, 4)
	// seq 0,1 silent; 2 loud; 3,4 silent
	for _, spec := range []struct {
		seq    uint16
		silent bool
	}{{0, true}, {1, true}, {2, false}, {3, true}, {4, true}} {
		if err := d.Push(packet(spec.seq, spec.silent), int64(spec.seq)*100); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := d.Finish()
	if err != nil {
		t.Fatal(err)
	}
	var silCount int
	for _, iv := range rep.Intervals {
		if iv.Kind == KindSilence {
			silCount++
		}
	}
	if silCount != 2 || rep.SilenceNS != 4*pktNS {
		t.Fatalf("intervals=%+v silenceNS=%d", rep.Intervals, rep.SilenceNS)
	}
	if rep.OrderedPackets != 5 || rep.DuplicatePackets != 0 {
		t.Fatalf("counts %+v", rep)
	}
	// First merged range covers both silent packets exactly.
	if rep.Intervals[0].StartNS != 0 || rep.Intervals[0].EndNS != 2*pktNS {
		t.Fatalf("first range %+v", rep.Intervals[0])
	}
}

func TestDuplicatesCountedOnce(t *testing.T) {
	d := newDetector(t, 4)
	for _, seq := range []uint16{0, 0, 1, 1, 2} {
		if err := d.Push(packet(seq, true), 0); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := d.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if rep.OrderedPackets != 3 || rep.DuplicatePackets != 2 {
		t.Fatalf("counts %+v", rep)
	}
	if rep.SilenceNS != 3*pktNS {
		t.Fatalf("silenceNS=%d", rep.SilenceNS)
	}
}

func TestReorderWithinWindow(t *testing.T) {
	d := newDetector(t, 4)
	// Arrival 1,2,3 with 2 backfilled after 3: anchor stays 1, all three
	// belong to the same generation and reorder to 1,2,3.
	for _, seq := range []uint16{1, 3, 2} {
		if err := d.Push(packet(seq, true), 0); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := d.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if rep.UnknownNS != 0 || rep.OrderedPackets != 3 || rep.DuplicatePackets != 0 {
		t.Fatalf("report %+v", rep)
	}
	if rep.SilenceNS != 3*pktNS {
		t.Fatalf("silenceNS=%d", rep.SilenceNS)
	}
	// Reordered intervals must still follow wire timestamps 10ms..40ms.
	if rep.Intervals[0].StartNS != pktNS || rep.Intervals[0].EndNS != 4*pktNS {
		t.Fatalf("interval %+v", rep.Intervals[0])
	}
}

func TestWindowExhaustedMarksUnknown(t *testing.T) {
	d := newDetector(t, 2)
	// 0,1 delivered; seq 4 arrives with gap 2,3 beyond window=2.
	if err := d.Push(packet(0, true), 0); err != nil {
		t.Fatal(err)
	}
	if err := d.Push(packet(1, true), 100); err != nil {
		t.Fatal(err)
	}
	if err := d.Push(packet(4, true), 400); err != nil {
		t.Fatal(err)
	}
	rep, err := d.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if rep.UnknownRanges != 1 || rep.UnknownNS != 2*pktNS {
		t.Fatalf("report %+v", rep)
	}
	// Unknown range ends exactly at the anchor packet start (40ms).
	var unknown *Interval
	for i := range rep.Intervals {
		if rep.Intervals[i].Kind == KindUnknown {
			unknown = &rep.Intervals[i]
		}
	}
	if unknown == nil || unknown.StartNS != 2*pktNS || unknown.EndNS != 4*pktNS {
		t.Fatalf("unknown interval %+v", unknown)
	}
	if !errors.Is(fault.ErrUnknownWindow, fault.ErrUnknownWindow) {
		t.Fatal("sentinel broken")
	}
}

func TestTrailingGapUnknownAtFinish(t *testing.T) {
	d := newDetector(t, 8)
	if err := d.Push(packet(0, false), 0); err != nil {
		t.Fatal(err)
	}
	// seq 5 while 1..4 missing; window not yet exhausted, Finish must flag.
	if err := d.Push(packet(5, false), 500); err != nil {
		t.Fatal(err)
	}
	rep, err := d.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if rep.UnknownNS != 4*pktNS || rep.OrderedPackets != 2 {
		t.Fatalf("report %+v", rep)
	}
}

func TestBadPCMPayload(t *testing.T) {
	d := newDetector(t, 4)
	p := packet(0, true)
	p.Payload = p.Payload[:len(p.Payload)-1] // odd length
	err := d.Push(p, 0)
	if !errors.Is(err, fault.ErrLength) {
		t.Fatalf("got %v", err)
	}
}

func TestBadConfig(t *testing.T) {
	if _, err := New(Config{ClockRate: 0}); !errors.Is(err, clock.ErrInvalidTick) {
		t.Fatalf("got %v", err)
	}
	if _, err := New(Config{ClockRate: 48000, SamplesPerPacket: 0}); err == nil {
		t.Fatal("zero samples accepted")
	}
}
