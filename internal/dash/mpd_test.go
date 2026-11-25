package dash

import (
	"strings"
	"testing"

	"mediascan/internal/errs"
)

const goodMPD = `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" minBufferTime="PT1.5S">
  <Period>
    <AdaptationSet mimeType="video/mp4">
      <Representation id="v0" mimeType="video/mp4">
        <SegmentTemplate timescale="90000" media="$RepresentationID$-$Number$.m4s" startNumber="1">
          <SegmentTimeline>
            <S t="0" d="90000"/>
            <S d="90000"/>
            <S d="45000" r="1"/>
          </SegmentTimeline>
        </SegmentTemplate>
      </Representation>
      <Representation id="v1" mimeType="video/mp4">
        <SegmentTemplate timescale="30000" media="$RepresentationID$-$Number$.m4s" startNumber="1">
          <SegmentTimeline>
            <S t="0" d="30000"/>
            <S d="30000"/>
            <S d="15000" r="1"/>
          </SegmentTimeline>
        </SegmentTemplate>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>`

func TestParseGoodMPD(t *testing.T) {
	m, err := Parse("sample.mpd", []byte(goodMPD))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if m.MinBufferTimeMS != 1500 {
		t.Fatalf("minBufferTime = %d, want 1500", m.MinBufferTimeMS)
	}
	if len(m.Representations) != 2 {
		t.Fatalf("representations = %d, want 2", len(m.Representations))
	}
	v0 := m.Representations[0]
	if v0.Timescale != 90000 || len(v0.Segments) != 4 {
		t.Fatalf("v0 = %+v", v0)
	}
	// r="1" duplicates the S element once, times advance by d.
	if v0.Segments[3].StartTime != 225000 || v0.Segments[3].Duration != 45000 {
		t.Fatalf("last segment = %+v", v0.Segments[3])
	}
	if v0.Segments[0].URL != "v0-1.m4s" {
		t.Fatalf("template expansion = %q", v0.Segments[0].URL)
	}
	start, end := v0.TimeRange()
	if start != 0 || end != 270000 {
		t.Fatalf("time range = [%d,%d), want [0,270000)", start, end)
	}
	v1 := m.Representations[1]
	if v1.Timescale != 30000 || v1.Segments[0].StartTime != 0 {
		t.Fatalf("v1 = %+v", v1)
	}
}

func TestDuplicateSegmentTime(t *testing.T) {
	bad := `<?xml version="1.0"?>
<MPD><Period><AdaptationSet>
  <Representation id="v0">
    <SegmentTemplate timescale="90000" startNumber="1">
      <SegmentTimeline>
        <S t="0" d="90000"/>
        <S t="90000" d="90000"/>
        <S t="90000" d="45000"/>
      </SegmentTimeline>
    </SegmentTemplate>
  </Representation>
</AdaptationSet></Period></MPD>`
	_, err := Parse("dup.mpd", []byte(bad))
	se, ok := errs.AsError(err)
	if !ok || se.Kind != errs.KindSegment || !strings.Contains(se.Message, "duplicate") {
		t.Fatalf("want duplicate KindSegment error, got %v", err)
	}
	if se.Offset == errs.Unknown {
		t.Fatal("duplicate error must carry a byte offset")
	}
}

func TestOverlapAndGap(t *testing.T) {
	overlap := `<?xml version="1.0"?>
<MPD><Period><AdaptationSet><Representation id="v0">
  <SegmentTemplate timescale="90000" startNumber="1">
    <SegmentTimeline>
      <S t="0" d="90000"/>
      <S t="50000" d="90000"/>
    </SegmentTimeline>
  </SegmentTemplate>
</Representation></AdaptationSet></Period></MPD>`
	_, err := Parse("overlap.mpd", []byte(overlap))
	se, _ := errs.AsError(err)
	if se == nil || se.Kind != errs.KindSegment || !strings.Contains(se.Message, "overlap") {
		t.Fatalf("want overlap error, got %v", err)
	}

	gap := `<?xml version="1.0"?>
<MPD><Period><AdaptationSet><Representation id="v0">
  <SegmentTemplate timescale="90000" startNumber="1">
    <SegmentTimeline>
      <S t="0" d="90000"/>
      <S t="95000" d="90000"/>
    </SegmentTimeline>
  </SegmentTemplate>
</Representation></AdaptationSet></Period></MPD>`
	_, err = Parse("gap.mpd", []byte(gap))
	se, _ = errs.AsError(err)
	if se == nil || !strings.Contains(se.Message, "gap") {
		t.Fatalf("want gap error, got %v", err)
	}
}

func TestBadXMLReportsOffset(t *testing.T) {
	_, err := Parse("broken.mpd", []byte("<MPD><Period>\n  <Representation>"))
	se, ok := errs.AsError(err)
	if !ok || se.Kind != errs.KindManifest || se.Offset == errs.Unknown {
		t.Fatalf("want located manifest error, got %v", err)
	}
}

func TestInvalidTimescale(t *testing.T) {
	bad := strings.Replace(goodMPD, `timescale="90000"`, `timescale="0"`, 1)
	_, err := Parse("zero.mpd", []byte(bad))
	se, _ := errs.AsError(err)
	if se == nil || se.Kind != errs.KindManifest {
		t.Fatalf("want manifest error for zero timescale attr, got %v", err)
	}
}

func TestSegmentBaseByteRange(t *testing.T) {
	mpd := `<MPD><Period><AdaptationSet>
	  <Representation id="a" mimeType="video/mp4">
	    <SegmentBase timescale="90000" mediaRange="1024-8191"/>
	  </Representation>
	</AdaptationSet></Period></MPD>`
	m, err := Parse("base.mpd", []byte(mpd))
	if err != nil {
		t.Fatal(err)
	}
	br := m.Representations[0].Segments[0].ByteRange
	if br == nil || br.Start != 1024 || br.End != 8192 {
		t.Fatalf("byte range = %+v, want [1024,8192)", br)
	}

	bad := strings.Replace(mpd, `mediaRange="1024-8191"`, `mediaRange="8191-1024"`, 1)
	if _, err = Parse("badrange.mpd", []byte(bad)); err == nil {
		t.Fatal("inverted byte range must be rejected")
	}
}

func TestParseDurationMS(t *testing.T) {
	cases := map[string]int64{
		"PT2S":    2000,
		"PT1.5S":  1500,
		"PT0.250S": 250,
	}
	for in, want := range cases {
		got, err := parseDuration(in)
		if err != nil || got != want {
			t.Fatalf("parseDuration(%q) = %d,%v want %d", in, got, err, want)
		}
	}
	if _, err := parseDuration("PX"); err == nil {
		t.Fatal("invalid duration must error")
	}
}
