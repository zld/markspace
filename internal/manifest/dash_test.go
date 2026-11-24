package manifest

import (
	"fmt"
	"strings"
	"testing"

	"mediad/internal/model"
)

const goodMPD = `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT10S">
  <Period>
    <AdaptationSet contentType="video">
      <Representation id="v-high">
        <SegmentTemplate timescale="1000" startNumber="1"
          initialization="v-high/init.mp4" media="v/$Number$.m4s">
          <SegmentTimeline>
            <S t="0" d="2000" r="4"/>
          </SegmentTimeline>
        </SegmentTemplate>
      </Representation>
      <Representation id="v-low">
        <SegmentTemplate timescale="1000" startNumber="1" media="l/$Number$.m4s">
          <SegmentTimeline>
            <S t="0" d="2000" r="4"/>
          </SegmentTimeline>
        </SegmentTemplate>
      </Representation>
    </AdaptationSet>
    <AdaptationSet contentType="audio">
      <Representation id="a-1">
        <SegmentTemplate timescale="48000" startNumber="1" media="a/$Number$.m4s">
          <SegmentTimeline>
            <S t="0" d="96000" r="4"/>
          </SegmentTimeline>
        </SegmentTemplate>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>`

func TestDASHHappyPath(t *testing.T) {
	m, err := ParseDASH([]byte(goodMPD), "sample.mpd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Duration != 10_000_000_000 {
		t.Fatalf("duration = %d", m.Duration)
	}
	if len(m.Representations) != 3 {
		t.Fatalf("reps = %d", len(m.Representations))
	}
	vh := m.Representations[0]
	if len(vh.Segments) != 5 {
		t.Fatalf("segments = %d", len(vh.Segments))
	}
	if vh.Segments[4].URL != "v/5.m4s" {
		t.Fatalf("template expansion = %q", vh.Segments[4].URL)
	}
	if vh.Segments[1].Start != 2_000_000_000 || vh.Segments[1].Duration != 2_000_000_000 {
		t.Fatalf("segment 1 range = %d/%d", vh.Segments[1].Start, vh.Segments[1].Duration)
	}
	if a := m.Representations[2]; a.Kind != model.TrackAudio || a.TimeScale != 48000 {
		t.Fatalf("audio rep misclassified: %+v", a)
	}
	ratio, matched, total := RoundtripConsistency(m)
	if ratio != 1.0 || matched != 15 || total != 15 {
		t.Fatalf("roundtrip = %f (%d/%d)", ratio, matched, total)
	}
}

func TestDASHZeroTimescale(t *testing.T) {
	bad := strings.Replace(goodMPD, `id="v-low">
        <SegmentTemplate timescale="1000"`,
		`id="v-low">
        <SegmentTemplate timescale="0"`, 1)
	_, err := ParseDASH([]byte(bad), "sample.mpd")
	se, ok := model.AsSource(err)
	if !ok {
		t.Fatalf("expected sourced error, got %v", err)
	}
	if se.Kind != model.KindZeroTimescale || se.Loc.Representation != "v-low" {
		t.Fatalf("kind/loc = %s %+v", se.Kind, se.Loc)
	}
}

func TestDASHMissingDuration(t *testing.T) {
	noDur := strings.Replace(goodMPD, `mediaPresentationDuration="PT10S"`, "", 1)
	_, err := ParseDASH([]byte(noDur), "sample.mpd")
	se, ok := model.AsSource(err)
	if !ok || se.Kind != model.KindMissingDuration {
		t.Fatalf("expected missing-duration, got %v", err)
	}
	noTimeline := strings.Replace(goodMPD,
		`<SegmentTimeline>
            <S t="0" d="2000" r="4"/>
          </SegmentTimeline>`, "", 1)
	_, err = ParseDASH([]byte(noTimeline), "sample.mpd")
	se, ok = model.AsSource(err)
	if !ok || se.Kind != model.KindMissingDuration {
		t.Fatalf("empty timeline must fail, got %v", err)
	}
}

func TestDASHIndexOutOfRange(t *testing.T) {
	// Segment ending beyond the presentation duration.
	over := strings.Replace(goodMPD, `<S t="0" d="2000" r="4"/>`,
		`<S t="0" d="4000" r="4"/>`, 1)
	_, err := ParseDASH([]byte(over), "sample.mpd")
	se, ok := model.AsSource(err)
	if !ok || se.Kind != model.KindIndexOutOfRange {
		t.Fatalf("expected index-out-of-range, got %v", err)
	}
	if se.Loc.Entry < 0 || se.Loc.Representation == "" {
		t.Fatalf("error must carry entry and representation: %+v", se.Loc)
	}
}

func TestDASHMissingFile(t *testing.T) {
	_, err := ParseDASHFile("definitely/missing.mpd")
	se, ok := model.AsSource(err)
	if !ok || se.Kind != model.KindManifestMissing {
		t.Fatalf("expected manifest-missing, got %v", err)
	}
}

func TestParseISODuration(t *testing.T) {
	cases := map[string]model.PlayTime{
		"PT10S":      10_000_000_000,
		"PT2.500S":   2_500_000_000,
		"PT1M":       60_000_000_000,
		"PT1H30M2S":  5402_000_000_000,
		"PT0.000001S": 1000,
	}
	for in, want := range cases {
		got, err := ParseISODuration(in)
		if err != nil || got != want {
			t.Errorf("ParseISODuration(%q) = %d, %v want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "10S", "PT", "PTXS", "P12"} {
		if _, err := ParseISODuration(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestDASHDecimalTickExactness(t *testing.T) {
	// 48 kHz representation: 2 s = 96000 ticks must map exactly back.
	m, err := ParseDASH([]byte(goodMPD), "x.mpd")
	if err != nil {
		t.Fatal(err)
	}
	a := m.Representations[2]
	seg := a.Segments[0]
	if got, _ := model.Unscale(seg.Duration, a.TimeScale); got != seg.DurationTicks {
		t.Fatalf("audio tick round trip broken: %d vs %d", got, seg.DurationTicks)
	}
	if fmt.Sprint(seg.Start) == "" {
		t.Fatal("unreachable")
	}
}
