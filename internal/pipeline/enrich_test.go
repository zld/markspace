package pipeline

import (
	"testing"

	"liveregress/internal/clock"
	"liveregress/internal/fault"
)

// TestMediaFaultsCarryTrackAndTime verifies that faults raised while reading a
// named track carry that track and, where known, a media time.
func TestMediaFaultsCarryTrackAndTime(t *testing.T) {
	fe := fault.New(fault.KindCorrupt, "v0/seg2.m4s", 1184, "bad sample").
		WithTrack(1, "video").AtTime(clock.Must(180000, 90000)).InLayer("v0").Recover(true)
	msg := fe.Error()
	for _, want := range []string{"track=1(video)", "time=2.000000s", "layer=v0", "offset=1184"} {
		if !contains(msg, want) {
			t.Fatalf("%q missing %q", msg, want)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
