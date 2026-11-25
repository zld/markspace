package hls

import (
	"strings"
	"testing"

	"mediascan/internal/clock"
	"mediascan/internal/errs"
)

const goodPlaylist = "#EXTM3U\n" +
	"#EXT-X-VERSION:3\n" +
	"#EXT-X-TARGETDURATION:6\n" +
	"#EXT-X-INDEPENDENT-SEGMENTS\n" +
	"#EXTINF:6.0,\nseg1.ts\n" +
	"#EXTINF:5.5,\nseg2.ts\n" +
	"#EXT-X-DISCONTINUITY\n" +
	"#EXTINF:4.25,new period\nseg3.ts\n" +
	"#EXT-X-BYTERANGE:1024@8192\n" +
	"#EXTINF:2.0,\nseg4.ts\n" +
	"#EXT-X-ENDLIST\n"

func TestParseGoodPlaylist(t *testing.T) {
	p, err := Parse("demo.m3u8", []byte(goodPlaylist))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if p.TargetDurMS != 6000 || p.Version != 3 || !p.Independent || !p.Endlist {
		t.Fatalf("header fields wrong: %+v", p)
	}
	if len(p.Segments) != 4 {
		t.Fatalf("segments = %d, want 4", len(p.Segments))
	}
	wantDur := []int64{6000, 5500, 4250, 2000}
	for i, want := range wantDur {
		if p.Segments[i].DurationMS != want {
			t.Fatalf("seg %d duration = %d, want %d", i, p.Segments[i].DurationMS, want)
		}
	}
	// Discontinuity advances, never resets, the same timeline.
	if !p.Segments[2].Discontinuity || p.Segments[1].Discontinuity {
		t.Fatal("discontinuity flag must land only on seg3")
	}
	r3, err := p.TimelineRange(2)
	if err != nil || r3 != (clock.Range{Start: 11500, End: 15750}) {
		t.Fatalf("seg3 range = %+v err=%v, want [11500,15750)", r3, err)
	}
	if p.TotalDurationMS() != 17750 {
		t.Fatalf("total = %d, want 17750", p.TotalDurationMS())
	}
	offs := p.DiscontinuityOffsets()
	if len(offs) != 1 {
		t.Fatalf("discontinuity offsets = %v", offs)
	}
	// The recorded offset must point at the actual tag bytes.
	if string(p.Raw()[offs[0]:offs[0]+int64(len("#EXT-X-DISCONTINUITY"))]) != "#EXT-X-DISCONTINUITY" {
		t.Fatalf("offset %d does not point at discontinuity tag", offs[0])
	}
	br := p.Segments[3].ByteRange
	if br == nil || br.Start != 8192 || br.End != 9216 {
		t.Fatalf("byte range = %+v", br)
	}
}

func TestMissingExtinf(t *testing.T) {
	bad := "#EXTM3U\n#EXTINF:2.0,\na.ts\norphan.ts\n"
	_, err := Parse("orphan.m3u8", []byte(bad))
	se, ok := errs.AsError(err)
	if !ok || se.Kind != errs.KindManifest || se.Offset == errs.Unknown {
		t.Fatalf("want located manifest error, got %v", err)
	}
	if !strings.Contains(se.Message, "EXTINF") {
		t.Fatalf("message should mention EXTINF: %v", err)
	}
}

func TestMissingDurationValue(t *testing.T) {
	bad := "#EXTM3U\n#EXTINF:\nx.ts\n"
	_, err := Parse("nodur.m3u8", []byte(bad))
	se, _ := errs.AsError(err)
	if se == nil || se.Kind != errs.KindManifest {
		t.Fatalf("want manifest error, got %v", err)
	}
}

func TestUnknownTagAndDoubleHash(t *testing.T) {
	bad := "#EXTM3U\n#EXT-X-MADE-UP-TAG:1\n#EXTINF:1.0,\nx.ts\n"
	if _, err := Parse("unknown.m3u8", []byte(bad)); err == nil {
		t.Fatal("unknown tag must be rejected")
	}
	bad2 := "#EXTM3U\n##BROKEN\n#EXTINF:1.0,\nx.ts\n"
	_, err2 := Parse("broken.m3u8", []byte(bad2))
	se, _ := errs.AsError(err2)
	if se == nil || se.Offset == errs.Unknown {
		t.Fatalf("double-hash line must produce located error, got %v", se)
	}
}

func TestExtinfDecimalNoFloatDrift(t *testing.T) {
	// 334 ms pieces accumulate exactly; 10 * 334 = 3340 with no rounding.
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:1\n")
	for i := 0; i < 10; i++ {
		b.WriteString("#EXTINF:0.334,\n")
		b.WriteString("seg.ts\n")
	}
	p, err := Parse("frac.m3u8", []byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if p.TotalDurationMS() != 3340 {
		t.Fatalf("total = %d, want 3340 (no float drift)", p.TotalDurationMS())
	}
}

func TestDanglingExtinf(t *testing.T) {
	bad := "#EXTM3U\n#EXTINF:2.0,\n"
	_, err := Parse("dangle.m3u8", []byte(bad))
	se, _ := errs.AsError(err)
	if se == nil || !strings.Contains(se.Message, "without a following segment URI") {
		t.Fatalf("want dangling EXTINF error, got %v", err)
	}
}

func TestTimelineRangeOutOfBounds(t *testing.T) {
	p, err := Parse("d.m3u8", []byte("#EXTM3U\n#EXTINF:1.0,\na.ts\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.TimelineRange(5); err == nil {
		t.Fatal("out-of-range segment lookup must error")
	}
}
