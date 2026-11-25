package continuity

import (
	"strings"
	"testing"
)

const sampleMPD = `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT6S">
  <Period id="p0" start="PT0S">
    <AdaptationSet contentType="video" mimeType="video/mp4" codecs="avc1.64001f" width="1280" height="720">
      <Representation id="v1" bandwidth="1200000">
        <SegmentTemplate timescale="90000" initialization="$RepresentationID$/init.mp4" media="$RepresentationID$/seg-$Number%05d$.m4s" startNumber="1">
          <SegmentTimeline>
            <S t="0" d="180000" r="2"/>
          </SegmentTimeline>
        </SegmentTemplate>
      </Representation>
    </AdaptationSet>
    <AdaptationSet contentType="audio" mimeType="audio/mp4" codecs="mp4a.40.2">
      <Representation id="a1" bandwidth="128000">
        <SegmentTemplate timescale="48000" initialization="a1/init.mp4" media="a1/seg-$Time$.m4s" startNumber="1">
          <SegmentTimeline>
            <S t="0" d="96000" r="1"/>
            <S d="48000"/>
          </SegmentTimeline>
        </SegmentTemplate>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>
`

func TestParseDASHBasic(t *testing.T) {
	m, err := ParseDASH("mpd/index.mpd", []byte(sampleMPD))
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != ManifestDASH || len(m.Representations) != 2 {
		t.Fatalf("kind=%s reps=%d", m.Kind, len(m.Representations))
	}
	v := m.Representations[0]
	if v.Kind != MediaVideo || v.Timescale != 90000 || v.Width != 1280 || v.Height != 720 {
		t.Fatalf("video rep=%+v", v)
	}
	if v.InitURI != "v1/init.mp4" {
		t.Fatalf("init=%q", v.InitURI)
	}
	if len(v.Segments) != 3 {
		t.Fatalf("video segments=%d", len(v.Segments))
	}
	if v.Segments[0].URI != "v1/seg-00001.m4s" || v.Segments[2].URI != "v1/seg-00003.m4s" {
		t.Fatalf("uris=%q,%q", v.Segments[0].URI, v.Segments[2].URI)
	}
	if v.Segments[1].Start != 2*Ticks(BaseTimescale) || v.Segments[2].Duration != 2*Ticks(BaseTimescale) {
		t.Fatalf("timing=%+v", v.Segments)
	}
	a := m.Representations[1]
	if a.Kind != MediaAudio || len(a.Segments) != 3 {
		t.Fatalf("audio=%+v", a)
	}
	if a.Segments[2].URI != "a1/seg-192000.m4s" {
		t.Fatalf("audio uri=%q", a.Segments[2].URI)
	}
	if a.Segments[2].Start != 4*Ticks(BaseTimescale) {
		t.Fatalf("audio start=%d", a.Segments[2].Start)
	}
}

func TestParseDASHPresentationTimeOffset(t *testing.T) {
	mpd := `<MPD type="static" mediaPresentationDuration="PT4S"><Period start="PT0S">
<AdaptationSet contentType="video">
<Representation id="v" bandwidth="1"><SegmentTemplate timescale="1000" media="s-$Number$.m4s" presentationTimeOffset="10000">
<SegmentTimeline><S t="12000" d="4000"/></SegmentTimeline></SegmentTemplate></Representation>
</AdaptationSet></Period></MPD>`
	m, err := ParseDASH("m.mpd", []byte(mpd))
	if err != nil {
		t.Fatal(err)
	}
	seg := m.Representations[0].Segments[0]
	if seg.Start != 2*Ticks(BaseTimescale) {
		t.Fatalf("start=%d want 180000", seg.Start)
	}
	if seg.Duration != 4*Ticks(BaseTimescale) {
		t.Fatalf("duration=%d", seg.Duration)
	}
}

func TestParseDASHErrors(t *testing.T) {
	cases := []struct {
		name string
		mpd  string
		kind Kind
	}{
		{"malformed", `<MPD><Period></MPD>`, KindParse},
		{"wrong root", `<MDP/>`, KindParse},
		{"dynamic", `<MPD type="dynamic"><Period/></MPD>`, KindUnsupported},
		{"no template", `<MPD type="static"><Period><AdaptationSet contentType="video"><Representation id="r" bandwidth="1"/></AdaptationSet></Period></MPD>`, KindUnsupported},
		{"segment list", `<MPD type="static"><Period><AdaptationSet contentType="video"><Representation id="r" bandwidth="1"><SegmentList/></Representation></AdaptationSet></Period></MPD>`, KindUnsupported},
		{"no timeline", `<MPD type="static"><Period><AdaptationSet contentType="video"><Representation id="r" bandwidth="1"><SegmentTemplate media="$Number$"/></Representation></AdaptationSet></Period></MPD>`, KindUnsupported},
		{"path escape", `<MPD type="static"><Period><AdaptationSet contentType="video"><Representation id="r" bandwidth="1"><SegmentTemplate media="../../x-$Number$.m4s"><SegmentTimeline><S t="0" d="1"/></SegmentTimeline></SegmentTemplate></Representation></AdaptationSet></Period></MPD>`, KindPathEscape},
		{"unknown placeholder", `<MPD type="static"><Period><AdaptationSet contentType="video"><Representation id="r" bandwidth="1"><SegmentTemplate media="x-$Foo$.m4s"><SegmentTimeline><S t="0" d="1"/></SegmentTimeline></SegmentTemplate></Representation></AdaptationSet></Period></MPD>`, KindParse},
		{"zero duration", `<MPD type="static"><Period><AdaptationSet contentType="video"><Representation id="r" bandwidth="1"><SegmentTemplate media="x-$Number$.m4s"><SegmentTimeline><S t="0" d="0"/></SegmentTimeline></SegmentTemplate></Representation></AdaptationSet></Period></MPD>`, KindParse},
		{"overlap", `<MPD type="static"><Period><AdaptationSet contentType="video"><Representation id="r" bandwidth="1"><SegmentTemplate media="x-$Number$.m4s"><SegmentTimeline><S t="0" d="90000"/><S t="45000" d="90000"/></SegmentTimeline></SegmentTemplate></Representation></AdaptationSet></Period></MPD>`, KindBadReference},
	}
	for _, tc := range cases {
		_, err := ParseDASH("m.mpd", []byte(tc.mpd))
		if err == nil {
			t.Errorf("%s: expected failure", tc.name)
			continue
		}
		if !IsKind(err, tc.kind) {
			t.Errorf("%s: got %v want kind %v", tc.name, err, tc.kind)
		}
		if loc := LocationOf(err); loc.File != "m.mpd" || loc.Offset < 0 {
			t.Errorf("%s: missing byte offset in %+v", tc.name, loc)
		}
	}
}

func TestParseDASHReportByteOffset(t *testing.T) {
	mpd := `<MPD type="static"><Period><AdaptationSet contentType="video">` +
		`<Representation id="r" bandwidth="1"><SegmentTemplate media="../../x-$Number$.m4s">` +
		`<SegmentTimeline><S t="0" d="1"/></SegmentTimeline></SegmentTemplate></Representation>` +
		`</AdaptationSet></Period></MPD>`
	_, err := ParseDASH("m.mpd", []byte(mpd))
	if err == nil {
		t.Fatal("expected path escape failure")
	}
	loc := LocationOf(err)
	if loc.Offset <= 0 {
		t.Fatalf("expected positive byte offset, got %+v", loc)
	}
	want := int64(strings.Index(mpd, `$Number$`))
	if loc.Offset < want {
		t.Fatalf("offset %d precedes the offending template (index %d)", loc.Offset, want)
	}
	if !strings.HasPrefix(loc.String(), "m.mpd@") {
		t.Fatalf("location=%q", loc.String())
	}
}

func TestParseDASHNegativeRepeatNeedsDuration(t *testing.T) {
	mpd := `<MPD type="static"><Period><AdaptationSet contentType="video"><Representation id="r" bandwidth="1">` +
		`<SegmentTemplate media="x-$Number$.m4s"><SegmentTimeline><S t="0" d="90000" r="-1"/></SegmentTimeline>` +
		`</SegmentTemplate></Representation></AdaptationSet></Period></MPD>`
	_, err := ParseDASH("m.mpd", []byte(mpd))
	if err == nil || !IsKind(err, KindUnsupported) {
		t.Fatalf("expected unsupported, got %v", err)
	}
}

func TestFormatDecimal(t *testing.T) {
	cases := []struct {
		format string
		v      int64
		want   string
	}{
		{"%d", 7, "7"},
		{"%05d", 7, "00007"},
		{"%03d", 1234, "1234"},
		{"%1d", 5, "5"},
	}
	for _, tc := range cases {
		got, err := formatDecimal(tc.format, tc.v)
		if err != nil || got != tc.want {
			t.Errorf("formatDecimal(%q,%d)=%q,%v want %q", tc.format, tc.v, got, err, tc.want)
		}
	}
	for _, bad := range []string{"%x", "%", "%0", "%-5d"} {
		if _, err := formatDecimal(bad, 1); err == nil {
			t.Errorf("format %q unexpectedly accepted", bad)
		}
	}
}

func TestParseISO8601Duration(t *testing.T) {
	cases := []struct {
		in   string
		want Ticks
	}{
		{"PT2S", 2 * Ticks(BaseTimescale)},
		{"PT1M30S", 90 * Ticks(BaseTimescale)},
		{"PT0.5S", 45000},
		{"PT1H", 3600 * Ticks(BaseTimescale)},
		{"P1DT1S", (86400 + 1) * Ticks(BaseTimescale)},
	}
	for _, tc := range cases {
		got, err := parseISO8601Duration(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("parseISO8601Duration(%q)=%d,%v want %d", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "2S", "P", "PT1M1M", "P1Y", "PT"} {
		if _, err := parseISO8601Duration(bad); err == nil {
			t.Errorf("%q unexpectedly accepted", bad)
		}
	}
}

func TestPrimarySelection(t *testing.T) {
	mpd := `<MPD type="static"><Period><AdaptationSet contentType="video">
<Representation id="hi" bandwidth="9000"><SegmentTemplate media="hi-$Number$.m4s"><SegmentTimeline><S t="0" d="90000"/></SegmentTimeline></SegmentTemplate></Representation>
<Representation id="lo" bandwidth="100"><SegmentTemplate media="lo-$Number$.m4s"><SegmentTimeline><S t="0" d="90000"/></SegmentTimeline></SegmentTemplate></Representation>
</AdaptationSet></Period></MPD>`
	m, err := ParseDASH("m.mpd", []byte(mpd))
	if err != nil {
		t.Fatal(err)
	}
	prim := m.Primary()
	if len(prim) != 1 || prim[0].ID != "lo" {
		t.Fatalf("primary=%+v", prim)
	}
}
