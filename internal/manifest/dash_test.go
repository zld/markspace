package manifest

import (
	"strings"
	"testing"

	"mediabatch/internal/fault"
)

const sampleMPD = `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT12S">
  <Period id="p0" start="PT0S" duration="PT12S">
    <AdaptationSet id="video">
      <Representation id="v720" bandwidth="2000000">
        <SegmentTemplate timescale="1000" media="v720/seg-$Number$.m4s" startNumber="1">
          <SegmentTimeline>
            <S t="0" d="4000" r="2"/>
          </SegmentTimeline>
        </SegmentTemplate>
      </Representation>
      <Representation id="v360" bandwidth="800000">
        <SegmentTemplate timescale="1" media="v360/seg-$Number$.m4s" duration="4" startNumber="10"/>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>`

func TestParseDASHTimeline(t *testing.T) {
	m, err := ParseDASHBytes("dash/manifest.mpd", []byte(sampleMPD))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(m.Segments); got != 6 {
		t.Fatalf("segments=%d want 6", got)
	}
	// Segments are ordered by presentation time, then representation id.
	want := []struct{ rep, url, start, end string }{
		{"v360", "v360/seg-10.m4s", "0", "4"},
		{"v720", "v720/seg-1.m4s", "0", "4"},
		{"v360", "v360/seg-11.m4s", "4", "8"},
		{"v720", "v720/seg-2.m4s", "4", "8"},
		{"v360", "v360/seg-12.m4s", "8", "12"},
		{"v720", "v720/seg-3.m4s", "8", "12"},
	}
	for i, w := range want {
		got := m.Segments[i]
		if got.Representation != w.rep || got.URL != w.url || got.Start.String() != w.start || got.End.String() != w.end {
			t.Fatalf("segment %d = %s %s %s..%s", i, got.Representation, got.URL, got.Start, got.End)
		}
	}
	if m.Issues.Errors() != 0 {
		t.Fatalf("unexpected issues: %+v", m.Issues)
	}
}

func TestParseDASHMissingDurationAndBadRef(t *testing.T) {
	mpd := `<MPD><Period id="p0" start="PT0S" duration="PT8S">
<AdaptationSet>
  <Representation id="a"><SegmentTemplate media="a/$Number$.m4s"/></Representation>
</AdaptationSet>
<AdaptationSet>
  <Representation id="b"><BaseURL>../escape.mp4</BaseURL><SegmentBase/></Representation>
</AdaptationSet>
</Period></MPD>`
	m, err := ParseDASHBytes("dash/m.mpd", []byte(mpd))
	if err != nil {
		t.Fatal(err)
	}
	CheckReferences(m)
	codes := map[fault.Code]int{}
	for _, i := range m.Issues {
		codes[i.Code]++
		if i.Source.Loc.File != "dash/m.mpd" || i.Source.Loc.Line == 0 {
			t.Errorf("issue lacks traceable line: %+v", i)
		}
	}
	if codes[fault.CodeDashNoDuration] != 1 {
		t.Fatalf("missing duration count=%d issues=%+v", codes[fault.CodeDashNoDuration], m.Issues)
	}
	if codes[fault.CodeOutOfRangeRef] != 1 {
		t.Fatalf("out of range count=%d", codes[fault.CodeOutOfRangeRef])
	}
}

func TestParseDASHMalformedXML(t *testing.T) {
	m, err := ParseDASHBytes("x.mpd", []byte("<MPD><Period>"))
	if err == nil {
		t.Fatal("expected XML error")
	}
	if m.Issues.Errors() == 0 {
		t.Fatal("expected recorded parse issue")
	}
	if m.Issues[0].Source.Loc.File != "x.mpd" {
		t.Fatalf("loc=%+v", m.Issues[0].Source.Loc)
	}
}

func TestMultiPeriodBoundary(t *testing.T) {
	mpd := `<MPD><Period id="p0" start="PT0S" duration="PT4S">
<AdaptationSet><Representation id="r">
<SegmentTemplate media="s$Number$.m4s" duration="4" timescale="1" startNumber="1"/></Representation></AdaptationSet></Period>
<Period id="p1" start="PT4S" duration="PT4S">
<AdaptationSet><Representation id="r">
<SegmentTemplate media="s$Number$.m4s" duration="4" timescale="1" startNumber="2"/></Representation></AdaptationSet></Period></MPD>`
	m, err := ParseDASHBytes("multi.mpd", []byte(mpd))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Segments) != 2 {
		t.Fatalf("segments=%d", len(m.Segments))
	}
	disc := m.Discontinuities()
	if len(disc) != 1 || disc[0] != 1 {
		t.Fatalf("discontinuities=%v", disc)
	}
	if m.Segments[1].Start.String() != "4" {
		t.Fatalf("second period starts at %s", m.Segments[1].Start)
	}
	if !strings.Contains(m.Segments[1].URL, "s2") {
		t.Fatalf("url=%s", m.Segments[1].URL)
	}
}
