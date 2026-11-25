package hls

import (
	"testing"

	"mediasync/internal/model"
)

func playlistFixture() []byte {
	return []byte(`#EXTM3U
#EXT-X-TARGETDURATION:4
#EXTINF:2.0,video:video-hi:0:keyframe
seg-v-0.m4s
#EXTINF:2.0,video:video-hi:1
seg-v-1.m4s
#EXT-X-DISCONTINUITY
#EXTINF:1.0,video:video-hi:0:keyframe
seg-v-2.m4s
#EXTINF:1.0,subtitle:subs-core:0
seg-s-0.m4s
#EXT-X-DISCONTINUITY
#EXTINF:3.0,video:video-hi:0:keyframe
seg-v-3.m4s
`)
}

func TestParseDiscontinuityAdvancesPeriod(t *testing.T) {
	p, err := Parse(playlistFixture(), 90000)
	if err != nil {
		t.Fatal(err)
	}
	if p.DiscoCount != 2 {
		t.Fatalf("disco count = %d, want 2", p.DiscoCount)
	}
	if len(p.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", p.Errors)
	}
	if len(p.Segments) != 5 {
		t.Fatalf("segments = %d, want 5", len(p.Segments))
	}
	// First period: two 2s segments back to back.
	if p.Segments[0].PeriodID != 0 || p.Segments[0].StartNS != 0 ||
		p.Segments[0].DurationNS != 2_000_000_000 {
		t.Fatalf("first segment wrong: %+v", p.Segments[0])
	}
	if p.Segments[1].StartNS != 2_000_000_000 || p.Segments[1].Index != 1 {
		t.Fatalf("second segment wrong: %+v", p.Segments[1])
	}
	// After the discontinuity the local timeline restarts at zero and the
	// index sequence restarts too — no stitching across the boundary.
	after := p.Segments[2]
	if after.PeriodID != 1 || after.StartNS != 0 || after.Index != 0 {
		t.Fatalf("post-disco segment must restart timeline, got %+v", after)
	}
	if !after.Keyframe {
		t.Fatal("first segment after discontinuity must be a keyframe")
	}
	// Second discontinuity -> period 2, again restarting at zero.
	last := p.Segments[4]
	if last.PeriodID != 2 || last.StartNS != 0 || last.DurationNS != 3_000_000_000 {
		t.Fatalf("second post-disco segment wrong: %+v", last)
	}
	if string(last.Payload) != "seg-v-3.m4s" {
		t.Fatalf("URI payload not retained: %q", last.Payload)
	}
}

func TestParseRejectsBadPlaylist(t *testing.T) {
	if _, err := Parse(playlistFixture(), 0); model.Classify(err) != model.KindInvalidTimescale {
		t.Fatalf("zero timescale: want %s, got %v", model.KindInvalidTimescale, err)
	}
	bad := []byte(`#EXTM3U
#EXTINF:0.0,video:v:0
seg0.m4s
#EXTINF:1.0,video:v:0
orphan-uri.m4s
#EXTINF:-2.0,video:v:0
neg.m4s
#EXTINF:1.0,bogus:v:0
kind.m4s
#EXT-X-DISCONTINUITY
#EXTINF:1.0,video:v:0:keyframe
good.m4s
`)
	p, err := Parse(bad, 1000)
	if err != nil {
		t.Fatal(err)
	}
	// The well-formed 1s EXTINF survives, plus the post-disco segment.
	if len(p.Segments) != 2 {
		t.Fatalf("two well-formed segments should survive, got %+v", p.Segments)
	}
	if p.Segments[0].PeriodID != 0 || p.Segments[0].DurationNS != 1_000_000_000 {
		t.Fatalf("pre-disco survivor wrong: %+v", p.Segments[0])
	}
	if p.Segments[1].PeriodID != 1 || p.Segments[1].StartNS != 0 {
		t.Fatalf("post-disco segment must restart timeline: %+v", p.Segments[1])
	}
	// Each of the three bad EXTINF lines also leaves its URI orphaned, so the
	// three failures surface as six located errors.
	if len(p.Errors) != 6 {
		t.Fatalf("want 6 located errors, got %d: %v", len(p.Errors), p.Errors)
	}
	for _, e := range p.Errors {
		if e.Offset <= 0 || e.Subsystem != "hls" {
			t.Fatalf("error must carry hls line location: %v", e)
		}
	}
}

func TestParseDeterministicAndNoCrossPeriodStitch(t *testing.T) {
	first, _ := Parse(playlistFixture(), 90000)
	second, _ := Parse(playlistFixture(), 90000)
	if len(first.Segments) != len(second.Segments) {
		t.Fatal("segment count differs across runs")
	}
	for i := range first.Segments {
		a, b := first.Segments[i], second.Segments[i]
		if a.PeriodID != b.PeriodID || a.StartNS != b.StartNS || a.DurationNS != b.DurationNS {
			t.Fatalf("segment %d differs across runs", i)
		}
	}
	// The first segment of every discontinuity period must restart the local
	// timeline at zero; no period may inherit its predecessor's local start.
	seenPeriod := map[int]bool{}
	for _, s := range first.Segments {
		if seenPeriod[s.PeriodID] {
			continue
		}
		seenPeriod[s.PeriodID] = true
		if s.StartInScale != 0 || s.StartNS != 0 {
			t.Fatalf("first segment of period %d stitched at start %d",
				s.PeriodID, s.StartInScale)
		}
	}
}
