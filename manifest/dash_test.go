package manifest

import (
	"strings"
	"testing"
)

const testDASH = `<?xml version="1.0"?>
<MPD mediaPresentationDuration="PT8S">
  <Period>
    <AdaptationSet contentType="video" mimeType="video/mp4">
      <Representation id="low" bandwidth="500000">
        <SegmentTemplate timescale="1000" media="low/seg-$Number%02d$.m4s" startNumber="1">
          <SegmentTimeline>
            <S t="0" d="2000" r="3"/>
          </SegmentTimeline>
        </SegmentTemplate>
      </Representation>
      <Representation id="high" bandwidth="1500000">
        <SegmentTemplate timescale="48000" media="high/$RepresentationID$-$Time$.m4s">
          <SegmentTimeline>
            <S t="0" d="96000" r="3"/>
          </SegmentTimeline>
        </SegmentTemplate>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>`

func TestParseDASHMultipleRepresentations(t *testing.T) {
	m, err := ParseDASH([]byte(testDASH), "index.mpd")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Representations) != 2 {
		t.Fatalf("representations=%d", len(m.Representations))
	}
	low := m.Representations[0]
	if len(low.Segments) != 4 {
		t.Fatalf("low segments=%d", len(low.Segments))
	}
	if low.Segments[1].StartNS != 2_000_000_000 || low.Segments[3].DurationNS != 2_000_000_000 {
		t.Fatalf("bad low timing: %+v", low.Segments)
	}
	if low.Segments[0].URL != "low/seg-01.m4s" {
		t.Fatalf("template not expanded: %q", low.Segments[0].URL)
	}
	high := m.Representations[1]
	if high.Segments[2].StartNS != 4_000_000_000 {
		t.Fatalf("48k timeline should normalize to ns: %+v", high.Segments[2])
	}
	if !strings.HasPrefix(high.Segments[0].URL, "high/high-0.m4s") {
		t.Fatalf("RepresentationID/Time expansion wrong: %q", high.Segments[0].URL)
	}
}

func TestParseDASHMalformedLocation(t *testing.T) {
	_, err := ParseDASH([]byte("<MPD><Period></MPD>"), "bad.mpd")
	if err == nil || !strings.Contains(err.Error(), "bad.mpd") {
		t.Fatalf("expected sourced XML error, got %v", err)
	}
}
