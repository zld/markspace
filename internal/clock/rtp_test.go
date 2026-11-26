package clock

import (
	"errors"
	"testing"

	"github.com/pion/rtp"
)

func TestFromRTPRational(t *testing.T) {
	pkt := &rtp.Packet{Header: rtp.Header{Timestamp: 270000}}
	tm, r, err := FromRTP(pkt, 90000)
	if err != nil {
		t.Fatal(err)
	}
	if tm.Ticks != 270000 || tm.Timescale != 90000 {
		t.Fatalf("time = %+v", tm)
	}
	if r.Compare(MustRational(3, 1)) != 0 {
		t.Fatalf("rational = %v", r)
	}
	if got := r.Ticks(48000); got != 144000 {
		t.Fatalf("ticks at 48k = %d", got)
	}
}

func TestFromRTPRejectsZeroRate(t *testing.T) {
	if _, _, err := FromRTP(&rtp.Packet{}, 0); !errors.Is(err, ErrRTP) {
		t.Fatalf("want ErrRTP, got %v", err)
	}
}

func TestRTPClockDetectsLossAndJumps(t *testing.T) {
	c := NewRTPClock(90000)
	for i, seq := range []uint16{100, 101, 104, 105} {
		pkt := &rtp.Packet{Header: rtp.Header{SequenceNumber: seq, Timestamp: uint32(90000 * i)}}
		if err := c.Add(pkt); err != nil {
			t.Fatal(err)
		}
	}
	if c.SequenceGaps != 2 {
		t.Fatalf("sequence gaps = %d", c.SequenceGaps)
	}
	if len(c.TimestampJumps) != 0 {
		t.Fatalf("unexpected timestamp jumps: %v", c.TimestampJumps)
	}
	elapsed, ok := c.Elapsed()
	if !ok || elapsed.Ticks != 270000 {
		t.Fatalf("elapsed = %+v ok=%v", elapsed, ok)
	}
}

func TestRTPClockRejectsRegression(t *testing.T) {
	c := NewRTPClock(90000)
	c.Add(&rtp.Packet{Header: rtp.Header{SequenceNumber: 5}})
	if err := c.Add(&rtp.Packet{Header: rtp.Header{SequenceNumber: 5}}); !errors.Is(err, ErrRTP) {
		t.Fatalf("want ErrRTP, got %v", err)
	}
}
