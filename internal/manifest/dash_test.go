package manifest

import (
	"strings"
	"testing"
)

const mpdFixture = `<?xml version="1.0" encoding="UTF-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT12.003S" profiles="urn:mpeg:dash:profile:isoff-live:2011">
  <Period id="p0">
    <AdaptationSet id="v" mimeType="video/mp4" codecs="avc1.64001f">
      <SegmentTemplate timescale="90000" initialization="$RepresentationID$/init.mp4" media="$RepresentationID$/seg-$Number$.m4s" startNumber="1">
        <SegmentTimeline>
          <S t="0" d="270270" r="2"/>
          <S d="269460"/>
        </SegmentTimeline>
      </SegmentTemplate>
      <Representation id="v720" bandwidth="800000" width="1280" height="720"/>
      <Representation id="v360" bandwidth="400000" width="640" height="360"/>
    </AdaptationSet>
  </Period>
</MPD>
`

func TestParseDASHTimelineTimescaleAndOrder(t *testing.T) {
	pl, err := ParseDASH("normal.mpd", []byte(mpdFixture), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pl.Representations) != 2 {
		t.Fatalf("want 2 representations, got %d", len(pl.Representations))
	}
	rep := pl.Representations[0]
	if rep.ID != "v720" || rep.Timescale != 90000 || rep.InitURI != "v720/init.mp4" {
		t.Fatalf("representation = %+v", rep)
	}
	if len(rep.Segments) != 4 {
		t.Fatalf("want 4 segments, got %d", len(rep.Segments))
	}
	if rep.Segments[0].URI != "v720/seg-1.m4s" || rep.Segments[3].URI != "v720/seg-4.m4s" {
		t.Fatalf("segment URIs: %q ... %q", rep.Segments[0].URI, rep.Segments[3].URI)
	}
	if rep.Segments[1].StartNs != 3_003_000_000 {
		t.Fatalf("segment 1 start = %d", rep.Segments[1].StartNs)
	}
	if rep.TotalNs != 12_003_000_000 {
		t.Fatalf("total = %d", rep.TotalNs)
	}
}

const noDurationMPD = `<?xml version="1.0"?>
<MPD type="static"><Period><AdaptationSet mimeType="video/mp4">
<SegmentTemplate timescale="90000" media="$Number$.m4s"/>
<Representation id="v" bandwidth="800000"/>
</AdaptationSet></Period></MPD>`

func TestParseDASHRejectsZeroTimescaleAndBadDurations(t *testing.T) {
	cases := map[string]string{
		"zero timescale": strings.Replace(mpdFixture, `timescale="90000"`, `timescale="0"`, 1),
		"bad duration":   strings.Replace(mpdFixture, `d="270270"`, `d="0"`, 1),
		"malformed xml":  strings.Replace(mpdFixture, `</MPD>`, ``, 1),
		"no duration":    noDurationMPD,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			pl, err := ParseDASH("bad.mpd", []byte(body), Limits{})
			if err == nil {
				t.Fatalf("expected error, got %+v", pl)
			}
			if !strings.Contains(err.Error(), "bad.mpd") {
				t.Fatalf("error lacks path: %v", err)
			}
		})
	}
}
