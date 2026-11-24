package clock

import "testing"

func TestSeqUnwrapperWrap(t *testing.T) {
	var u SeqUnwrapper
	got := u.Update(65534)
	if got != 65534 {
		t.Fatalf("first = %d, want 65534", got)
	}
	cases := []struct {
		raw  uint16
		want uint64
	}{
		{65535, 65535},
		{0, 65536},
		{1, 65537},
		{0, 65536}, // replayed/duplicate raw value resolves back to extended 65536
		{2, 65538},
	}
	for _, c := range cases {
		if got := u.Update(c.raw); got != c.want {
			t.Errorf("Update(%d) = %d, want %d", c.raw, got, c.want)
		}
	}
}

func TestTSUnwrapperWrap(t *testing.T) {
	var u TSUnwrapper
	_ = u.Update(0xfffffff0)
	cases := []struct {
		raw  uint32
		want uint64
	}{
		{0xfffffff8, 0xfffffff8},
		{0x00000000, 1 << 32},
		{0x00000008, 1<<32 + 8},
		{0xfffffff0, 0xfffffff0}, // late packet before the wrap
		{0x00000010, 1<<32 + 16},
	}
	for _, c := range cases {
		if got := u.Update(c.raw); got != c.want {
			t.Errorf("Update(%#x) = %d, want %d", c.raw, got, c.want)
		}
	}
}

func TestClockMapRateConversion(t *testing.T) {
	cm := NewClockMap(24000)
	pairs := []struct {
		src, media uint64
	}{
		{0, 0},
		{2400, 4800},
		{4800, 9600},
	}
	for _, p := range pairs {
		if got := cm.Position(p.src); got != p.media {
			t.Errorf("Position(%d) = %d, want %d", p.src, got, p.media)
		}
	}
}

func TestClockMapSRDriftAndNoRegression(t *testing.T) {
	cm := NewClockMap(MediaClockHz)
	for ts := uint64(0); ts <= 1000; ts += 100 {
		if got := cm.Position(ts); got != ts {
			t.Fatalf("pre-SR Position(%d) = %d, want %d", ts, got, ts)
		}
	}
	// Late SR: one wall second (NTP 1.000s) claims RTP tick 1000.
	ntpOneSec := uint64(1) << 32
	cm.ApplySR(1000, ntpOneSec)
	if got := cm.Position(1100); got != 1100 {
		t.Errorf("post-SR Position(1100) = %d, want 1100", got)
	}
	// Freeze the SR instant, then deliver a late, contradictory SR whose
	// implied position would move time backwards.
	cm.Freeze(1000)
	cm.ApplySR(1200, ntpOneSec) // same wall time, higher RTP tick
	if got := cm.Position(1200); got < 1000 {
		t.Fatalf("timeline regressed to %d after late SR", got)
	}
	// Two seconds of wall time at the next SR must advance by 96000 ticks.
	cm.ApplySR(2000, uint64(3)<<32)
	if got := cm.Position(2000); got != 1000+2*48000 {
		t.Errorf("drift-corrected Position(2000) = %d, want %d", got, 1000+2*48000)
	}
}

func TestTimelineMonotonicAndFreeze(t *testing.T) {
	tl := NewTimeline()
	add := func(seq uint16, ts uint32) TimelineEntry {
		return tl.Add(PacketRef{Seq: seq, RTPTS: ts, ClockRate: MediaClockHz, Offset: int64(seq) * 100})
	}
	add(0, 0)
	e1 := add(1, 48)
	if e1.ExtSeq != 1 || e1.MediaPos != 48 {
		t.Fatalf("entry1 = %+v", e1)
	}
	tl.Commit(1)
	// A late packet with an earlier timestamp must not move time backwards.
	late := add(2, 24)
	if late.MediaPos < e1.MediaPos {
		t.Fatalf("late packet regressed media %d < %d", late.MediaPos, e1.MediaPos)
	}
	if tl.CommittedSeq() != 1 {
		t.Fatalf("committed seq = %d, want 1", tl.CommittedSeq())
	}
	if len(tl.Entries()) != 3 {
		t.Fatalf("entries = %d, want 3", len(tl.Entries()))
	}
	// Wrap across the 16-bit sequence boundary; timestamps stay linear
	// in their 32-bit domain and the TS unwrapper carries the high bits.
	tl2 := NewTimeline()
	add2 := func(seq uint16, ts uint32) TimelineEntry {
		return tl2.Add(PacketRef{Seq: seq, RTPTS: ts, ClockRate: MediaClockHz, Offset: int64(seq)})
	}
	add2(0xfffe, 0xffffffe0)    // anchor packet -> media position 0
	add2(0xffff, 0xfffffff0)    // +16 source ticks
	wrap := add2(0, 0x00000000) // +16 more across the 32-bit wrap
	if wrap.ExtSeq != 65536 {
		t.Fatalf("post-wrap ext seq = %d, want 65536", wrap.ExtSeq)
	}
	// Media positions are relative to the anchor packet: 0x100000000-0xffffffe0.
	if want := uint64(0x100000000) - 0xffffffe0; wrap.MediaPos != want {
		t.Fatalf("post-wrap media = %d, want %d", wrap.MediaPos, want)
	}
}
