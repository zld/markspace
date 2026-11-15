package receive

import (
	"testing"

	"streamlab/internal/jitter"
	"streamlab/internal/wire"
)

func newTestPipeline() *Pipeline {
	return NewPipeline(20, 1, 4, jitter.Config{FrameMS: 20, Target: 2, MinLevel: 1, MaxLevel: 8}, 16, 3)
}

func datagram(seq uint16, fill int16) []byte {
	return wire.Encode(&wire.Packet{
		StreamID:  1,
		Seq:       seq,
		Timestamp: uint32(seq) * 20,
		Samples:   []int16{fill, fill, fill, fill},
	})
}

// feed delivers datagrams for the given seqs at 30ms transit delay and
// advances the clock far enough to play out want frames.
func feed(p *Pipeline, seqs []uint16, want int) {
	t := int64(0)
	for _, s := range seqs {
		arr := int64(s)*20 + 30
		for ; t < arr; t++ {
			p.AdvanceTo(t)
		}
		p.OnDatagram(datagram(s, 100), arr)
	}
	for len(p.Events) < want && t < 100000 {
		t++
		p.AdvanceTo(t)
	}
}

func TestPipelineConsecutiveLossConcealment(t *testing.T) {
	p := newTestPipeline()
	// Deliver 0,1 then a 6-frame gap, then 8,9.
	feed(p, []uint16{0, 1, 8, 9}, 10)
	if p.Received != 4 {
		t.Fatalf("received %d want 4", p.Received)
	}
	concealed := 0
	for _, ev := range p.Events {
		if ev.Concealed {
			concealed++
		}
		if ev.Watermark < 0 {
			t.Fatalf("negative watermark at seq %d", ev.Seq)
		}
	}
	if concealed != 6 {
		t.Fatalf("concealed %d want 6", concealed)
	}
	total, maxRun, _, multi, jumps := p.Concealer().Metrics()
	if total != 6 || maxRun != 6 {
		t.Fatalf("conceal total=%d maxRun=%d want 6/6", total, maxRun)
	}
	if multi != 1 {
		t.Fatalf("multi runs %d want 1", multi)
	}
	if len(jumps) == 0 {
		t.Fatal("no boundary energy jumps recorded")
	}
}

func TestPipelineBoundedConcealment(t *testing.T) {
	p := newTestPipeline() // maxConceal = 3
	feed(p, []uint16{0, 10}, 11)
	// Runs longer than 3 frames must decay to silence.
	var lastConcealed *Event
	for i := range p.Events {
		if p.Events[i].Concealed {
			lastConcealed = &p.Events[i]
		}
	}
	if lastConcealed == nil || lastConcealed.Energy != 0 {
		t.Fatalf("long gap not bounded to silence: %+v", lastConcealed)
	}
}

func TestPipelineBadPacketDoesNotStopLoop(t *testing.T) {
	p := newTestPipeline()
	p.OnDatagram([]byte{0x01, 0x02}, 0) // too short
	p.OnDatagram(datagram(0, 100), 30)
	feed(p, []uint16{1, 2}, 3)
	if p.Dropped["too_short"] != 1 {
		t.Fatalf("dropped %v", p.Dropped)
	}
	if p.Received != 3 || len(p.Events) != 3 {
		t.Fatalf("received %d events %d", p.Received, len(p.Events))
	}
}

func TestPipelineDuplicateAndLateCounted(t *testing.T) {
	p := newTestPipeline()
	feed(p, []uint16{0, 1, 2}, 3)
	p.OnDatagram(datagram(1, 100), 500) // duplicate, already played
	p.OnDatagram(datagram(0, 100), 500) // late, behind cursor
	if p.Dropped["duplicate"]+p.Dropped["late"] != 2 {
		t.Fatalf("dropped %v", p.Dropped)
	}
	if p.Received != 5 {
		t.Fatalf("received %d want 5 (duplicates counted pre-window)", p.Received)
	}
}

func TestConcealerRunsAndDecay(t *testing.T) {
	c := NewConcealer(2)
	c.Real([]int16{1000, 1000})
	a := c.Conceal(2)
	b := c.Conceal(2)
	d := c.Conceal(2) // beyond maxRun: silence
	if Energy(a) >= Energy([]int16{1000, 1000}) || Energy(b) >= Energy(a) {
		t.Fatal("concealment does not decay")
	}
	if Energy(d) != 0 {
		t.Fatal("concealment not bounded to silence")
	}
	c.Real([]int16{500, 500})
	c.Conceal(2)
	c.Flush()
	total, maxRun, single, multi, jumps := c.Metrics()
	if total != 4 || maxRun != 3 || single != 1 || multi != 1 {
		t.Fatalf("metrics total=%d max=%d single=%d multi=%d", total, maxRun, single, multi)
	}
	if len(jumps) < 3 {
		t.Fatalf("jumps %v", jumps)
	}
}
