package clock

import (
	"math"
	"testing"

	"replay/internal/fault"
)

func mustClock(t *testing.T, track string, rate uint32) *TrackClock {
	t.Helper()
	c, err := New(track, rate)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestMapWithRTCPAnchorIsExact(t *testing.T) {
	c := mustClock(t, "audio", 48000)
	if err := c.AnchorRTCP(1000, 5_000_000_000); err != nil {
		t.Fatalf("AnchorRTCP: %v", err)
	}
	if c.Source() != SourceRTCP {
		t.Fatalf("source=%s want rtcp", c.Source())
	}
	pt, err := c.Map(Tick{Timestamp: 1000 + 48000, Seq: 7})
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if pt.At != 6_000_000_000 {
		t.Fatalf("at=%d want 6e9", pt.At)
	}
	if pt.Estimated {
		t.Fatal("rtcp-anchored point must not be estimated")
	}
	if pt.Ticks != 48000 {
		t.Fatalf("ticks=%d", pt.Ticks)
	}
}

func TestFirstPacketAnchorMarksEstimated(t *testing.T) {
	c := mustClock(t, "video", 90000)
	if err := c.AnchorFirstPacket(4000, 0); err != nil {
		t.Fatalf("AnchorFirstPacket: %v", err)
	}
	pt, err := c.Map(Tick{Timestamp: 4000 + 900, Seq: 1})
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if pt.At != 10_000_000 {
		t.Fatalf("at=%d want 10ms", pt.At)
	}
	if !pt.Estimated || pt.Source != SourceFirstPacket {
		t.Fatalf("estimated=%v source=%s", pt.Estimated, pt.Source)
	}
}

func TestMapHandlesForwardWrap(t *testing.T) {
	c := mustClock(t, "audio", 48000)
	if err := c.AnchorRTCP(0xffff0000, 0); err != nil {
		t.Fatalf("anchor: %v", err)
	}
	// Cross the 32-bit boundary: 0x4000 ticks after the anchor wraps.
	pt, err := c.Map(Tick{Timestamp: 0x00004000, Seq: 2})
	if err != nil {
		t.Fatalf("Map after wrap: %v", err)
	}
	if pt.Extended != 0x1_0000_4000 {
		t.Fatalf("extended=%x want 0x100004000", pt.Extended)
	}
	if pt.Ticks != 0x4000+0x10000 {
		t.Fatalf("ticks=%d", pt.Ticks)
	}
	// Slots are 0x10000 apart, so the expected instant is 0x14000 ticks.
	want := int64((0x14000 * 1_000_000_000) / 48000)
	if pt.At != want {
		t.Fatalf("at=%d want %d", pt.At, want)
	}
}

func TestMapRejectsRegression(t *testing.T) {
	c := mustClock(t, "audio", 48000)
	if err := c.AnchorFirstPacket(1000, 0); err != nil {
		t.Fatalf("anchor: %v", err)
	}
	if _, err := c.Map(Tick{Timestamp: 1000 + 480, Seq: 5}); err != nil {
		t.Fatalf("first map: %v", err)
	}
	_, err := c.Map(Tick{Timestamp: 1000, Seq: 4})
	if err == nil {
		t.Fatal("expected regression error")
	}
	fe, ok := fault.As(err)
	if !ok || fe.Kind != fault.KindClock {
		t.Fatalf("kind: %#v", err)
	}
	if fe.Track != "audio" || !fe.HasSeq || fe.Seq != 4 {
		t.Fatalf("missing location: %s", fe.Error())
	}
}

func TestMapRejectsOverflow(t *testing.T) {
	c := mustClock(t, "audio", 1)
	// Anchor one second below the int64 ceiling at 1 Hz; any further
	// tick pushes the unified instant past the ceiling.
	if err := c.AnchorFirstPacket(0, math.MaxInt64-1_000_000_000); err != nil {
		t.Fatalf("anchor: %v", err)
	}
	if _, err := c.Map(Tick{Timestamp: 2, Seq: 1}); err == nil {
		t.Fatal("expected overflow error")
	} else if fe, _ := fault.As(err); fe == nil || fe.Kind != fault.KindClock {
		t.Fatalf("kind: %#v", err)
	}
}

func TestOffsetRejectsHugeDelta(t *testing.T) {
	c := mustClock(t, "audio", 48000)
	if _, err := c.offsetNanoseconds(math.MaxInt64); err == nil {
		t.Fatal("expected conversion overflow")
	}
}

func TestAnchorTwiceRejected(t *testing.T) {
	c := mustClock(t, "audio", 48000)
	if err := c.AnchorRTCP(0, 0); err != nil {
		t.Fatalf("anchor: %v", err)
	}
	if err := c.AnchorRTCP(10, 1); err == nil {
		t.Fatal("second anchor should fail")
	}
}

func TestNTPToNanoseconds(t *testing.T) {
	// Unix epoch in NTP: seconds=2208988800, fraction=0.
	ns, err := NTPToNanoseconds(0x83aa7e80_00000000)
	if err != nil || ns != 0 {
		t.Fatalf("ns=%d err=%v", ns, err)
	}
	// One second after the Unix epoch.
	ns, err = NTPToNanoseconds(0x83aa7e81_00000000)
	if err != nil || ns != 1_000_000_000 {
		t.Fatalf("ns=%d err=%v", ns, err)
	}
	// Half a second via the fraction field.
	ns, err = NTPToNanoseconds(0x83aa7e80_80000000)
	if err != nil || ns != 500_000_000 {
		t.Fatalf("ns=%d err=%v", ns, err)
	}
}

func TestSortPointsDeterministicTieBreak(t *testing.T) {
	pts := []TimelinePoint{
		{Track: "b", At: 10, Seq: 1},
		{Track: "a", At: 10, Seq: 2},
		{Track: "a", At: 10, Seq: 1},
		{Track: "a", At: 5, Seq: 9},
	}
	SortPoints(pts)
	want := []struct {
		track string
		at    int64
		seq   uint64
	}{{"a", 5, 9}, {"a", 10, 1}, {"a", 10, 2}, {"b", 10, 1}}
	for i, w := range want {
		if pts[i].Track != w.track || pts[i].At != w.at || pts[i].Seq != w.seq {
			t.Fatalf("index %d = %+v want %+v", i, pts[i], w)
		}
	}
}

func TestUnanchoredMapFails(t *testing.T) {
	c := mustClock(t, "audio", 48000)
	_, err := c.Map(Tick{Timestamp: 1})
	if err == nil || !fault.IsKind(err, fault.KindClock) {
		t.Fatalf("want clock error, got %v", err)
	}
}

func TestNewRejectsBadInput(t *testing.T) {
	if _, err := New("", 48000); !fault.IsKind(err, fault.KindArgument) {
		t.Fatalf("empty track: %v", err)
	}
	if _, err := New("a", 0); !fault.IsKind(err, fault.KindArgument) {
		t.Fatalf("zero rate: %v", err)
	}
}
