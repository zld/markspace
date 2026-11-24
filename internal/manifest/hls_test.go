package manifest

import (
	"strings"
	"testing"
)

func TestSecondsToUnifiedTicks(t *testing.T) {
	cases := map[string]int64{
		"1":      1_000_000,
		"4.000":  4_000_000,
		"0.033333": 33333,
		"0.0333333": 33333,  // 7th digit 3, no round
		"0.0333336": 33334,  // rounds up
	}
	for in, want := range cases {
		got, err := SecondsToUnifiedTicks(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got != want {
			t.Fatalf("%s = %d want %d", in, got, want)
		}
	}
}

func TestParseHLSDiscontinuityPeriods(t *testing.T) {
	pl := `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-TARGETDURATION:2
#EXT-X-MAP:URI="init.mp4"
#EXTINF:1.0,
a0.mp4
#EXTINF:1.0,
a1.mp4
#EXT-X-DISCONTINUITY
#EXTINF:1.5,
b0.mp4
#EXTINF:1.5,
b1.mp4
`
	h, err := ParseHLS(strings.NewReader(pl))
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Segments) != 4 {
		t.Fatalf("segments=%d", len(h.Segments))
	}
	if h.MapURI != "init.mp4" || h.Segments[0].MapURI != "init.mp4" {
		t.Fatalf("map not propagated: %q", h.MapURI)
	}
	if h.Segments[0].Period != 0 || h.Segments[1].Period != 0 {
		t.Fatal("first two segments should be period 0")
	}
	if h.Segments[2].Period != 1 || !h.Segments[2].Discontinuity {
		t.Fatal("b0 should open period 1 with discontinuity flag")
	}
	if h.Segments[3].Period != 1 || h.Segments[3].Discontinuity {
		t.Fatal("b1 stays in period 1 without its own flag")
	}
	if h.Segments[2].DurationTicks != 1_500_000 {
		t.Fatalf("duration=%d", h.Segments[2].DurationTicks)
	}
	// Ordered global axis preserves adjacency across the boundary.
	if h.Segments[2].GlobalStartTicks != 2_000_000 {
		t.Fatalf("global start after two 1s = %d", h.Segments[2].GlobalStartTicks)
	}
}

func TestParseHLSMissingHeader(t *testing.T) {
	_, err := ParseHLS(strings.NewReader("#EXTINF:1.0\na.mp4\n"))
	if err == nil {
		t.Fatal("expected missing EXTM3U error")
	}
}

func TestSafeResolveConfinement(t *testing.T) {
	root := "/tmp/material"
	ok, err := SafeResolve(root, "segments/a.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if ok != "/tmp/material/segments/a.mp4" {
		t.Fatalf("resolved=%s", ok)
	}
	for _, bad := range []string{"../etc/passwd", "/etc/passwd", "http://x/a.mp4", "../../oops.mp4"} {
		if _, err := SafeResolve(root, bad); err == nil {
			t.Fatalf("expected rejection of %q", bad)
		}
	}
}
