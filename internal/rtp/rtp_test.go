package rtp

import (
	"testing"
	"time"
)

func makeWire(t *testing.T, ssrc uint32, seq uint16, ts uint32, payload []byte) []byte {
	t.Helper()
	p := Packet{SSRC: ssrc, PT: 96, Seq: seq, RTPTime: ts, Payload: payload}
	w, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestUnmarshalMarshalRoundtrip(t *testing.T) {
	wire := makeWire(t, 0xdeadbeef, 1234, 99999, []byte{1, 2, 3, 4})
	p, err := Unmarshal(wire, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if p.SSRC != 0xdeadbeef || p.Seq != 1234 || p.RTPTime != 99999 || p.ArrivalNS != 1000 {
		t.Fatalf("parsed mismatch: %+v", p)
	}
	if _, err := Unmarshal([]byte{0x00}, 0); err == nil {
		t.Fatal("truncated datagram must fail")
	}
	nanos, per, err := ClockRate(90000)
	if err != nil || nanos != 100000 || per != 9 {
		t.Fatalf("90kHz rational tick = %d/%d %v", nanos, per, err)
	}
	nanos, per, err = ClockRate(48000)
	if err != nil || RTPToNanos(48, nanos, per) != 1_000_000 {
		t.Fatalf("48kHz conversion: %d/%d %v", nanos, per, err)
	}
	if _, _, err := ClockRate(0); err == nil {
		t.Fatal("zero clock rate must be rejected")
	}
}

func TestSequenceExtensionAndWrap(t *testing.T) {
	var e Extender
	seq := []uint16{65534, 65535, 0, 1, 2}
	want := []uint64{0, 1, 2, 3, 4}
	for i, s := range seq {
		if got := e.Extend(s); got != want[i] {
			t.Errorf("extend %d => %d, want %d", s, got, want[i])
		}
	}
	var e2 Extender
	e2.Extend(10)
	if got := e2.Extend(10); got != 0 { // duplicate stays at same ext
		t.Errorf("duplicate ext = %d, want 0", got)
	}
	if got := e2.Extend(9); got != ^uint64(0) { // backward jump => reset cycle
		t.Errorf("reset ext = %d, want max uint64", got)
	}
}

func TestDemuxSeparation(t *testing.T) {
	d := NewDemux()
	a := d.Push(Packet{SSRC: 1, PT: 96, Seq: 5})
	b := d.Push(Packet{SSRC: 2, PT: 96, Seq: 5})
	c := d.Push(Packet{SSRC: 1, PT: 96, Seq: 6})
	if a.ExtSeq != 0 || b.ExtSeq != 0 || c.ExtSeq != 1 {
		t.Fatalf("ext seqs: %d %d %d", a.ExtSeq, b.ExtSeq, c.ExtSeq)
	}
	if len(d.Streams()) != 2 {
		t.Fatalf("streams = %v", d.Streams())
	}
}

func TestJitterReorderDuplicateLate(t *testing.T) {
	buf := NewJitterBuffer(JitterConfig{Window: 4, MaxPackets: 64, TickNS: 1000}, nil)
	d := NewDemux()
	push := func(seq uint16, ts uint32) {
		p := d.Push(Packet{SSRC: 7, PT: 96, Seq: seq, RTPTime: ts, Nanos: int64(seq) * 1000})
		buf.Push(p)
	}
	// 0,1 then 3 (hole at 2), duplicate 3, late re-send of 0 after advance.
	push(0, 0)
	push(1, 1000)
	push(3, 3000)
	// window (4 slots) not yet reached: 0..3 pending with a hole at 2.
	push(3, 3000) // duplicate while pending
	// force window drain by arriving at sequence 5 (5-0>=4)
	push(5, 5000)
	events := buf.Flush()
	kinds := map[EventKind]int{}
	var order []uint64
	for _, ev := range events {
		kinds[ev.Kind]++
		if ev.Kind == EventPacket || ev.Kind == EventRecovered {
			order = append(order, ev.ExtSeq)
		}
	}
	if kinds[EventDuplicate] != 1 {
		t.Errorf("duplicates = %d, want 1", kinds[EventDuplicate])
	}
	if kinds[EventGap] < 2 { // seq 2 and seq 4 unrecoverable
		t.Errorf("gaps = %d, want >=2", kinds[EventGap])
	}
	// packets must come out in extended order
	for i := 1; i < len(order); i++ {
		if order[i] <= order[i-1] {
			t.Fatalf("out-of-order output: %v", order)
		}
	}
	// Out-of-window late arrival.
	late := d.Push(Packet{SSRC: 7, PT: 96, Seq: 0, RTPTime: 0, Nanos: 0, ExtSeq: 0})
	buf.Push(late)
	ev2 := buf.Flush()
	if len(ev2) != 1 || ev2[0].Kind != EventLate {
		t.Fatalf("late handling = %+v", ev2)
	}
}

func TestXORSingleRecovery(t *testing.T) {
	d := NewDemux()
	xr := NewXORRepair()
	// Group covers extended sequences 0,1,2; parity = XOR of their payloads.
	p10 := d.Push(Packet{SSRC: 9, PT: 96, Seq: 10, Payload: []byte{0x10, 0x20}})
	parity := []byte{0x10 ^ 0x30 ^ 0x50, 0x20 ^ 0x40 ^ 0x60}
	if err := xr.AddGroup(ParityGroup{Members: []uint64{0, 1, 2}, RTPTime: 11000, Parity: parity}); err != nil {
		t.Fatal(err)
	}
	buf := NewJitterBuffer(JitterConfig{Window: 4, MaxPackets: 64, TickNS: 1000}, xr)
	buf.Push(p10)
	p12 := Packet{SSRC: 9, PT: 96, Seq: 12, ExtSeq: 2, Nanos: 2000, Payload: []byte{0x50, 0x60}}
	// sequence 11 missing; window trigger drains through it.
	buf.Push(p12)
	buf.Push(Packet{SSRC: 9, PT: 96, Seq: 14, ExtSeq: 4, Nanos: 4000})
	events := buf.Flush()
	var recovered *Event
	gaps := 0
	for i := range events {
		if events[i].Kind == EventRecovered {
			recovered = &events[i]
		}
		if events[i].Kind == EventGap {
			gaps++
		}
	}
	if recovered == nil {
		t.Fatalf("expected recovery, events=%+v", events)
	}
	if string(recovered.Packet.Payload) != string([]byte{0x30, 0x40}) {
		t.Errorf("recovered payload = %x", recovered.Packet.Payload)
	}
	if recovered.ExtSeq != 1 || recovered.Nanos <= 0 {
		t.Errorf("recovery position = %+v", recovered)
	}
}

func TestXORDoubleLossBecomesGap(t *testing.T) {
	// Stream anchored at ext 0 present; ext 1 and ext 2 are both missing,
	// so a single-loss XOR group cannot save either one.
	xr2 := NewXORRepair()
	_ = xr2.AddGroup(ParityGroup{Members: []uint64{0, 1, 2}, RTPTime: 0,
		Parity: []byte{0xaa, 0xbb}})
	buf := NewJitterBuffer(JitterConfig{Window: 4, MaxPackets: 64, TickNS: 1000}, xr2)
	buf.Push(Packet{SSRC: 3, PT: 96, Seq: 10, ExtSeq: 0, Nanos: 0, Payload: []byte{1, 2}})
	buf.Push(Packet{SSRC: 3, PT: 96, Seq: 13, ExtSeq: 3, Nanos: 3000})
	events := buf.Flush()
	for _, ev := range events {
		if ev.Kind == EventRecovered {
			t.Fatalf("double loss must not be recovered: %+v", ev)
		}
	}
	// ext 1 and ext 2 are explicit holes.
	if got := CountStats(events).Gaps; got != 2 {
		t.Fatalf("expected 2 explicit gaps, got %d: %+v", got, events)
	}
}

// TestThroughput100k measures single-process ingest throughput against the
// 100,000 packets/second requirement (fixed seed, no wall-clock jitter).
func TestThroughput100k(t *testing.T) {
	const n = 100_000
	buf := NewJitterBuffer(JitterConfig{Window: 64, MaxPackets: 4096, TickNS: 1000}, nil)
	d := NewDemux()
	begin := time.Now().UnixNano()
	for i := 0; i < n; i++ {
		p := d.Push(Packet{SSRC: 42, PT: 97, Seq: uint16(i),
			ExtSeq: uint64(i), Nanos: int64(i) * 1000, Payload: []byte{byte(i)}})
		buf.Push(p)
	}
	events := buf.Flush()
	elapsed := time.Now().UnixNano() - begin
	if got := CountStats(events).Packets; got != n {
		t.Fatalf("ordered packets = %d, want %d", got, n)
	}
	pps := int64(n) * 1_000_000_000 / elapsed
	if pps < 100_000 {
		t.Fatalf("throughput %d packets/s below 100000 (elapsed %d ns)", pps, elapsed)
	}
	t.Logf("throughput=%d packets/s elapsed_ms=%d", pps, elapsed/1_000_000)
}

// TestBufferMemoryBound ensures hard packet caps force draining rather than
// unbounded growth under a pathological never-closing hole.
func TestBufferMemoryBound(t *testing.T) {
	buf := NewJitterBuffer(JitterConfig{Window: 8, MaxPackets: 16, TickNS: 1000}, nil)
	for i := uint64(0); i < 1000; i++ {
		// every packet is one window ahead of the next expected => gaps drain
		buf.Push(Packet{SSRC: 5, PT: 96, ExtSeq: i * 20, Nanos: int64(i) * 20_000})
	}
	for _, s := range buf.streams {
		if len(s.pending) > 16 {
			t.Fatalf("pending %d exceeds MaxPackets", len(s.pending))
		}
	}
	_ = buf.Flush()
}
