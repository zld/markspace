package jitter

import (
	"testing"
	"time"

	"jbbench/internal/conceal"
	"jbbench/internal/protocol"
)

func playerPkt(no int, arrival time.Duration) (protocol.Packet, time.Duration) {
	return protocol.Packet{
		Version: protocol.Version,
		Seq:     uint16(no),
		FrameNo: uint32(no),
		SendNS:  uint64(no) * uint64(protocol.FrameDurationNS()),
		Samples: make([]int16, protocol.SamplesPerFrame),
	}, arrival
}

func newTestPlayer() *Player {
	cfg := DefaultPlayerConfig()
	return NewPlayer(cfg)
}

func TestPlayerInOrderPlayback(t *testing.T) {
	p := newTestPlayer()
	frame := protocol.FrameDurationNS()
	for i := 0; i < 10; i++ {
		pkt, at := playerPkt(i, time.Duration(i)*frame)
		if r := p.Push(pkt, at); r != "" {
			t.Fatalf("push %d rejected: %s", i, r)
		}
	}
	// Anchor maps frame 0 send time to receive time 0.
	p.SetClockAnchor(0, 0)
	out := p.Drain(9 * frame)
	if len(out) != 10 {
		t.Fatalf("played %d frames, want 10", len(out))
	}
	for i, o := range out {
		if o.FrameNo != uint32(i) || o.Concealed {
			t.Fatalf("outcome %d = %+v", i, o)
		}
		if o.Frame.Kind != conceal.KindReal {
			t.Fatalf("frame %d kind = %s", i, o.Frame.Kind)
		}
		if o.Latency < 0 {
			t.Fatalf("negative latency: %v", o.Latency)
		}
	}
	if p.ReceivedCount() != 10 || p.PlayedCount() != 10 {
		t.Fatalf("counts: recv=%d played=%d", p.ReceivedCount(), p.PlayedCount())
	}
}

func TestPlayerConcealsGapsAndRecovers(t *testing.T) {
	p := newTestPlayer()
	frame := protocol.FrameDurationNS()
	// Frames 0, then gap 1-2, then frame 3.
	for _, no := range []int{0, 3} {
		pkt, at := playerPkt(no, time.Duration(no)*frame)
		if r := p.Push(pkt, at); r != "" {
			t.Fatalf("push %d: %s", no, r)
		}
	}
	p.SetClockAnchor(0, 0)
	out := p.Drain(3 * frame)
	if len(out) != 4 {
		t.Fatalf("played %d frames, want 4", len(out))
	}
	kinds := []conceal.Kind{
		conceal.KindReal, conceal.KindHold, conceal.KindAtten, conceal.KindRecover,
	}
	for i, want := range kinds {
		if out[i].Frame.Kind != want {
			t.Fatalf("frame %d kind = %s, want %s", i, out[i].Frame.Kind, want)
		}
	}
	if !out[1].Concealed || !out[2].Concealed || out[3].Concealed {
		t.Fatal("concealed flags wrong")
	}
	if p.ConcealedCount() != 2 {
		t.Fatalf("concealed = %d, want 2", p.ConcealedCount())
	}
}

func TestPlayerDuplicateLateAmbiguous(t *testing.T) {
	p := newTestPlayer()
	frame := protocol.FrameDurationNS()
	pkt0, _ := playerPkt(0, 0)
	p.Push(pkt0, 0)
	if r := p.Push(pkt0, frame); r != "duplicate" {
		t.Fatalf("dup reason = %q", r)
	}
	// Advance the cursor so frame 0 becomes late.
	p.SetClockAnchor(0, 0)
	p.Drain(0)
	if r := p.Push(pkt0, frame); r != "late" {
		t.Fatalf("late reason = %q", r)
	}
	// Exact half-interval sequence distance is invalid.
	amb := pkt0
	amb.Seq = pkt0.Seq + 32768
	amb.FrameNo = 100
	if r := p.Push(amb, frame); r != "ambiguous" {
		t.Fatalf("ambiguous reason = %q", r)
	}
	st := p.Window().Stats()
	if st.DuplicateDrops != 1 || st.LateDrops != 1 {
		t.Fatalf("window stats = %+v", st)
	}
}

func TestPlayerReorderCounting(t *testing.T) {
	p := newTestPlayer()
	frame := protocol.FrameDurationNS()
	order := []int{0, 2, 1, 4, 3}
	for _, no := range order {
		pkt, at := playerPkt(no, time.Duration(no)*frame)
		if r := p.Push(pkt, at); r != "" {
			t.Fatalf("push %d: %s", no, r)
		}
	}
	if p.ReorderedCount() != 2 {
		t.Fatalf("reordered = %d, want 2", p.ReorderedCount())
	}
	// The window's own counter uses a broader definition (any accepted frame
	// not landing on the play cursor) and is reported independently.
	if p.Window().Stats().Reordered < 2 {
		t.Fatalf("window reorder stat = %d", p.Window().Stats().Reordered)
	}
}

func TestPlayerFlushRemainingReportsGaps(t *testing.T) {
	p := newTestPlayer()
	frame := protocol.FrameDurationNS()
	for _, no := range []int{0, 1, 4} {
		pkt, at := playerPkt(no, time.Duration(no)*frame)
		p.Push(pkt, at)
	}
	p.SetClockAnchor(0, 0)
	out, gaps := p.FlushRemaining(0)
	if len(out) != 5 {
		t.Fatalf("flushed %d frames, want 5", len(out))
	}
	if gaps != 2 {
		t.Fatalf("pending gaps = %d, want 2", gaps)
	}
	if p.Window().Len() != 0 {
		t.Fatal("window not drained")
	}
}
