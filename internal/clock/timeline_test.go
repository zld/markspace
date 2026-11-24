package clock

import (
	"testing"

	"github.com/pion/rtp"
)

func TestUnwrap33Normal(t *testing.T) {
	mono, wrapped, disc := Unwrap33(1000, 1300)
	if mono != 1300 || wrapped || disc {
		t.Fatalf("got %d %v %v, want 1300 false false", mono, wrapped, disc)
	}
}

func TestUnwrap33Wrap(t *testing.T) {
	prev := Mod33 - 50
	mono, wrapped, disc := Unwrap33(prev, 100)
	want := prev + (100 + Mod33 - prev)
	if mono != want {
		t.Fatalf("mono = %d, want %d", mono, want)
	}
	if !wrapped || disc {
		t.Fatalf("got wrapped=%v disc=%v, want true false", wrapped, disc)
	}
	if mono <= prev {
		t.Fatalf("unwrapped time must move forward: %d <= %d", mono, prev)
	}
}

func TestUnwrap33Discontinuity(t *testing.T) {
	// A small backward step is not a wrap and must be flagged.
	_, wrapped, disc := Unwrap33(10000, 9000)
	if wrapped || !disc {
		t.Fatalf("got wrapped=%v disc=%v, want false true", wrapped, disc)
	}
	// A forward jump above one second is also a discontinuity.
	if _, _, disc = Unwrap33(0, Rate*5); !disc {
		t.Fatal("large forward jump must be a discontinuity")
	}
}

func TestTimelineFeedMonotonicAcrossWrap(t *testing.T) {
	tl := NewTimeline()
	first := tl.Feed(Mod33 - 100)
	second := tl.Feed(200)
	if second <= first {
		t.Fatalf("time went backwards: %d -> %d", first, second)
	}
	if len(tl.Errs) != 1 || tl.Errs[0].Kind != KindPCRWrap {
		t.Fatalf("want one pcr_wrap error, got %+v", tl.Errs)
	}
	if !tl.HasErrorAt((first + second) / 2) {
		t.Fatal("wrap range must cover the gap between samples")
	}
	if tl.Mono() != second {
		t.Fatalf("Mono = %d, want %d", tl.Mono(), second)
	}
}

func TestUnwrap32AndRTP(t *testing.T) {
	prev := uint32(Mod32 - 100)
	mono, wrapped := Unwrap32(int64(prev), 200)
	if !wrapped || mono != int64(prev)+300 {
		t.Fatalf("Unwrap32 = %d wrapped=%v", mono, wrapped)
	}
	// Same rule applied to a pion/rtp packet's 32-bit timestamp.
	pkt := &rtp.Packet{Header: rtp.Header{Timestamp: 1000}}
	rtpMono, rtpWrapped := UnwrapRTP(0, pkt)
	if rtpMono != 1000 || rtpWrapped {
		t.Fatalf("first RTP extend = %d wrapped=%v, want 1000 false", rtpMono, rtpWrapped)
	}
	// Monotonic advance near the 32-bit edge, then a wrap back to a low value.
	nearRaw := uint32(Mod32 - 100)
	pkt.Timestamp = nearRaw
	near, wrapped := UnwrapRTP(rtpMono, pkt)
	if wrapped || near != int64(nearRaw) {
		t.Fatalf("near-wrap = %d wrapped=%v, want %d false", near, wrapped, nearRaw)
	}
	pkt.Timestamp = 200
	rtpWrap, wrapped := UnwrapRTP(near, pkt)
	if rtpWrap != int64(Mod32)+200 || !wrapped {
		t.Fatalf("RTP wrap = %d wrapped=%v, want %d true", rtpWrap, wrapped, int64(Mod32)+200)
	}
	// Cross-check against the pion/rtp sequencer's own 16-bit rollover count.
	seq := rtp.NewFixedSequencer(0xffff)
	values, rollovers := SeqRollover(seq, 3)
	want := []uint16{0xffff, 0, 1}
	for i := range want {
		if values[i] != want[i] {
			t.Fatalf("seq[%d] = %d, want %d", i, values[i], want[i])
		}
	}
	if rollovers != 1 {
		t.Fatalf("rollover count = %d, want 1", rollovers)
	}
}

func TestMsConversions(t *testing.T) {
	if Ms(Rate) != 1000 {
		t.Fatalf("90000 ticks must be 1000 ms")
	}
	if FromMs(1000) != Rate {
		t.Fatalf("1000 ms must be 90000 ticks")
	}
	if Ms(45) != 1 {
		t.Fatalf("half-tick must round to 1 ms")
	}
	e := RangeError{Kind: KindSync, Start: 10, End: 20}
	if !e.Contains(10) || e.Contains(20) || e.Contains(9) {
		t.Fatal("range must be half-open [Start,End)")
	}
}
