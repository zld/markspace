package rtp

import "testing"

func TestTrackRegistryBindsReportToTimeline(t *testing.T) {
	reg := NewTrackRegistry(90000)
	tl := reg.Timeline(77)
	tl.Feed(5000)

	sr := SenderReport{SSRC: 77, RTPTimestamp: 5000, NTPSeconds: 12.5}
	if err := reg.ApplySenderReport(sr); err != nil {
		t.Fatal(err)
	}
	if !tl.Trusted() {
		t.Fatal("timeline must be trusted after the SR is applied")
	}
	wall, err := tl.ToWall(tl.Feed(14000)) // +9000 ticks = +0.1s
	if err != nil {
		t.Fatal(err)
	}
	if d := wall - 12.6; d > 1e-9 || d < -1e-9 {
		t.Fatalf("wall = %v, want 12.6", wall)
	}
}

func TestTrackRegistryRejectsUnknownSSRC(t *testing.T) {
	reg := NewTrackRegistry(90000)
	err := reg.ApplySenderReport(SenderReport{SSRC: 999, RTPTimestamp: 1})
	if err != ErrUnknownSSRC {
		t.Fatalf("err = %v, want ErrUnknownSSRC", err)
	}
}

func TestTrackRegistrySortedSSRCs(t *testing.T) {
	reg := NewTrackRegistry(90000)
	reg.Timeline(30)
	reg.Timeline(2)
	reg.Timeline(15)
	got := reg.SSRCs()
	want := []uint32{2, 15, 30}
	if len(got) != len(want) {
		t.Fatalf("SSRCs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SSRCs = %v, want %v", got, want)
		}
	}
}
