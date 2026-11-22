package manifest

import (
	"strings"
	"testing"

	"mediamux/media"
)

const hlsFixture = `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:10
#EXT-X-MAP:URI="init.mp4"
#EXTINF:6.006,
seg-10.m4s
#EXTINF:6.006,
seg-11.m4s
#EXT-X-DISCONTINUITY
#EXTINF:5.994,
seg-12.m4s
#EXT-X-ENDLIST
`

func TestParseHLSUnifiedTimeline(t *testing.T) {
	doc, err := ParseHLS(hlsFixture)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Kind != KindHLS || len(doc.Segments) != 3 {
		t.Fatalf("kind=%s segments=%d", doc.Kind, len(doc.Segments))
	}
	if doc.MediaSequence != 10 {
		t.Fatalf("sequence=%d", doc.MediaSequence)
	}
	if doc.Discontinuities != 1 || !doc.Segments[2].Discontinuity {
		t.Fatalf("discontinuity flags not propagated: %+v", doc.Segments)
	}
	if doc.Segments[0].Init != "init.mp4" {
		t.Fatalf("init map=%q", doc.Segments[0].Init)
	}
	// 6.006s at 90kHz is exactly 540540 ticks.
	if got := doc.Segments[0].Duration.Value; got != 540540 {
		t.Fatalf("duration ticks=%d", got)
	}
	if doc.Segments[1].Start.Value != 540540 || doc.Segments[2].Start.Value != 1081080 {
		t.Fatalf("starts not contiguous: %v %v", doc.Segments[1].Start, doc.Segments[2].Start)
	}
	if !doc.Alignment.Sum.IsZero() {
		t.Fatalf("expected zero drift, got %v", doc.Alignment.Sum)
	}
	if doc.Alignment.ErrorMS() > 2 {
		t.Fatalf("alignment error %dms exceeds 2ms", doc.Alignment.ErrorMS())
	}
	if doc.TotalQuantised().Cmp(doc.TotalDeclared()) != 0 {
		t.Fatalf("quantised %v != declared %v", doc.TotalQuantised(), doc.TotalDeclared())
	}
}

func TestParseHLSTargetDurationAndErrors(t *testing.T) {
	doc, err := ParseHLS(hlsFixture)
	if err != nil {
		t.Fatal(err)
	}
	if doc.TargetDuration.Num != 6 || doc.TargetDuration.Den != 1 {
		t.Fatalf("target=%v", doc.TargetDuration)
	}

	cases := map[string]string{
		"no header":    "#EXT-X-TARGETDURATION:6\n#EXTINF:6,\na.m4s\n",
		"no target":    "#EXTM3U\n#EXTINF:6,\na.m4s\n",
		"orphan seg":   "#EXTM3U\n#EXT-X-TARGETDURATION:6\na.m4s\n",
		"bad duration": "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:abc,\na.m4s\n",
		"trailing":     "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6,\n",
	}
	for name, src := range cases {
		if _, err := ParseHLS(src); err == nil {
			t.Fatalf("%s: expected error", name)
		} else if !media.HasCode(err, media.CodeManifestDrift) && !media.HasCode(err, media.CodeInput) {
			t.Fatalf("%s: unexpected error %v", name, err)
		}
	}
}

func TestParseHLSErrorCarriesLineOffset(t *testing.T) {
	src := "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:bad,\na.m4s\n"
	_, err := ParseHLS(src)
	se, ok := media.AsError(err)
	if !ok {
		t.Fatalf("want structured error, got %v", err)
	}
	want := int64(len("#EXTM3U\n#EXT-X-TARGETDURATION:6\n"))
	if se.Offset != want {
		t.Fatalf("offset=%d want %d", se.Offset, want)
	}
	if se.Phase != media.PhaseManifest {
		t.Fatalf("phase=%s", se.Phase)
	}
}

func TestAlignmentAccumulatesSubTickRemainder(t *testing.T) {
	// 3.333333s segments at 90kHz leave a real remainder every segment.
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:4\n")
	for i := 0; i < 9; i++ {
		b.WriteString("#EXTINF:3.333333,\nseg.m4s\n")
	}
	doc, err := ParseHLS(b.String())
	if err != nil {
		t.Fatal(err)
	}
	if doc.Alignment.Sum.IsZero() {
		t.Fatal("expected accumulated rational remainder")
	}
	// Nine segments each lose at most one tick; total drift stays under 1ms.
	if doc.Alignment.ErrorMS() > 1 {
		t.Fatalf("drift=%dms", doc.Alignment.ErrorMS())
	}
	// Timeline stays monotonic and contiguous.
	for i := 1; i < len(doc.Segments); i++ {
		if doc.Segments[i].Start.Value < doc.Segments[i-1].End().Value {
			t.Fatalf("segment %d overlaps previous", i)
		}
	}
}

const dashFixture = `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT18.018S">
  <Period id="p0" start="PT0S">
    <AdaptationSet contentType="video" mimeType="video/mp4">
      <SegmentTemplate timescale="90000" duration="540540" startNumber="7"
        initialization="init.mp4" media="seg-$Number$.m4s"/>
      <Representation id="v1" bandwidth="1200000" codecs="avc1.640028"/>
    </AdaptationSet>
  </Period>
</MPD>`

func TestParseDASHTemplate(t *testing.T) {
	doc, err := ParseDASH(dashFixture)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Kind != KindDASH || len(doc.Segments) != 3 {
		t.Fatalf("segments=%d", len(doc.Segments))
	}
	if doc.TargetBPS != 1200000 {
		t.Fatalf("bps=%d", doc.TargetBPS)
	}
	if doc.MediaSequence != 7 {
		t.Fatalf("start number=%d", doc.MediaSequence)
	}
	if doc.Segments[0].Duration.Value != 540540 {
		t.Fatalf("duration=%d", doc.Segments[0].Duration.Value)
	}
	if doc.Segments[2].Duration.Value != 540540 {
		t.Fatalf("last duration=%d", doc.Segments[2].Duration.Value)
	}
	if doc.Timescale != OutputTimescale {
		t.Fatalf("timescale=%d", doc.Timescale)
	}
}

func TestParseDASHSegmentTimeline(t *testing.T) {
	src := `<?xml version="1.0"?>
<MPD type="static">
  <Period id="p0" start="PT10S">
    <AdaptationSet contentType="video">
      <SegmentTemplate timescale="30000" startNumber="1" initialization="init.mp4">
        <SegmentTimeline>
          <S t="0" d="180180" r="1"/>
          <S d="90090"/>
        </SegmentTimeline>
      </SegmentTemplate>
      <Representation id="v1" bandwidth="800000"/>
    </AdaptationSet>
  </Period>
</MPD>`
	doc, err := ParseDASH(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Segments) != 3 {
		t.Fatalf("segments=%d", len(doc.Segments))
	}
	// 10s period offset plus 6.006s segments.
	if doc.Segments[0].Start.Value != 10*90000 {
		t.Fatalf("first start=%d", doc.Segments[0].Start.Value)
	}
	// 90090/30000 s == 3.003 s == 270270 ticks at 90kHz.
	if doc.Segments[2].Duration.Value != 270270 {
		t.Fatalf("last duration=%d", doc.Segments[2].Duration.Value)
	}
	if doc.TargetDuration.Cmp(media.NewRatio(180180, 30000)) != 0 {
		t.Fatalf("target=%v", doc.TargetDuration)
	}
}

func TestParseDASHErrors(t *testing.T) {
	cases := map[string]string{
		"empty":       "   ",
		"not xml":     "<MPD><Period>",
		"no template": `<MPD><Period><AdaptationSet><Representation id="v" bandwidth="1"/></AdaptationSet></Period></MPD>`,
		"no duration": `<MPD><Period><AdaptationSet><SegmentTemplate timescale="1000" startNumber="1"/><Representation id="v" bandwidth="1"/></AdaptationSet></Period></MPD>`,
	}
	for name, src := range cases {
		if _, err := ParseDASH(src); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}

func TestParseISODuration(t *testing.T) {
	cases := []struct {
		in  string
		num int64
		den int64
	}{
		{"PT6.006S", 3003, 500},
		{"PT1H2M3S", 3723, 1},
		{"PT0.5S", 1, 2},
		{"P1DT1S", 86401, 1},
		{"PT0S", 0, 1},
	}
	for _, c := range cases {
		got, err := parseISODuration(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if got.Cmp(media.NewRatio(c.num, c.den)) != 0 {
			t.Fatalf("%s: got %v want %d/%d", c.in, got, c.num, c.den)
		}
	}
	if _, err := parseISODuration("6s"); err == nil {
		t.Fatal("expected error for non-ISO duration")
	}
}

func TestFloorTicksKeepsExactResidual(t *testing.T) {
	r := media.NewRatio(1, 7)
	ticks, rem, err := floorTicks(r, 90)
	if err != nil {
		t.Fatal(err)
	}
	if ticks != 12 { // floor(90/7)
		t.Fatalf("ticks=%d", ticks)
	}
	want := media.NewRatio(1, 7).Sub(media.NewRatio(12, 90))
	if rem.Cmp(want) != 0 {
		t.Fatalf("rem=%v want %v", rem, want)
	}
}
