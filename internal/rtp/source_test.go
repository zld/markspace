package rtp

import (
	"math"
	"testing"
	"time"
)

func TestBuildScheduleMarksDropAndDelay(t *testing.T) {
	packets, sched, err := BuildSchedule(Config{
		Packets:    240,
		Interval:   20 * time.Millisecond,
		DropEvery:  37,
		DelayEvery: 29,
		Delay:      7 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sched.Generated != 240 {
		t.Fatalf("generated=%d", sched.Generated)
	}
	if sched.Skipped != 6 {
		t.Fatalf("skipped=%d want 6", sched.Skipped)
	}
	if sched.Delayed != 8 {
		t.Fatalf("delayed=%d want 8", sched.Delayed)
	}
	skipped := 0
	for _, p := range packets {
		if p.Skip {
			skipped++
		}
		if p.RTP.PayloadType != PayloadType {
			t.Fatalf("payload type=%d", p.RTP.PayloadType)
		}
	}
	if skipped != 6 {
		t.Fatalf("skip flags=%d", skipped)
	}
	if !packets[len(packets)-1].RTP.Marker {
		t.Fatal("last packet should carry the marker bit")
	}
}

func TestBuildScheduleSequenceAndTimestampWrap(t *testing.T) {
	packets, sched, err := BuildSchedule(Config{
		Packets:        5,
		Interval:       20 * time.Millisecond,
		StartSequence:  math.MaxUint16 - 1,
		StartTimestamp: math.MaxUint32 - 3600,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sched.EndSeq != 2 {
		t.Fatalf("end sequence=%d want 2", sched.EndSeq)
	}
	want := []uint32{math.MaxUint32 - 3600, math.MaxUint32 - 1800, math.MaxUint32, 1799, 3599}
	for i, p := range packets {
		if p.RTP.Timestamp != want[i] {
			t.Fatalf("packet %d timestamp=%d want %d", i, p.RTP.Timestamp, want[i])
		}
	}
}

func TestBuildScheduleRejectsInvalid(t *testing.T) {
	cases := []Config{
		{Packets: 0, Interval: time.Millisecond},
		{Packets: 1, Interval: 0},
		{Packets: 2, Interval: time.Millisecond, DropEvery: 1},
		{Packets: 2, Interval: time.Millisecond, DelayEvery: 1},
		{Packets: 2, Interval: time.Millisecond, Delay: -1},
	}
	for _, c := range cases {
		if _, _, err := BuildSchedule(c); err == nil {
			t.Fatalf("expected error for %+v", c)
		}
	}
}

func TestWrapHelpers(t *testing.T) {
	if got := SequenceDelta(65535, 1); got != 2 {
		t.Fatalf("sequence delta=%d", got)
	}
	if got := TimestampDelta(math.MaxUint32-1, 1); got != 3 {
		t.Fatalf("timestamp delta=%d", got)
	}
	if got := WrapSequence(65534, 4); got != 2 {
		t.Fatalf("wrapped sequence=%d", got)
	}
	if got := WrapTimestamp(math.MaxUint32-1, 3); got != 1 {
		t.Fatalf("wrapped timestamp=%d", got)
	}
}
