package manifest

import (
	"strings"
	"testing"

	"archivecheck/internal/model"
)

const dashSample = `<?xml version="1.0" encoding="UTF-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static"
     mediaPresentationDuration="PT0H0M15.000S" minBufferTime="PT2S">
  <Period id="p0" start="PT0S">
    <AdaptationSet id="1" contentType="video" mimeType="video/mp4">
      <SegmentTemplate timescale="90000" duration="450000" startNumber="1"
                       media="$RepresentationID$/seg-$Number$.m4s"
                       initialization="$RepresentationID$/init.mp4"/>
      <Representation id="v-high" bandwidth="3000000"/>
      <Representation id="v-low" bandwidth="600000"/>
    </AdaptationSet>
  </Period>
</MPD>
`

func TestParseDASHTemplateDuration(t *testing.T) {
	plan, err := ParseDASH("manifest.mpd", strings.NewReader(dashSample), Options{})
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	if plan.Timebase != 90000 {
		t.Fatalf("timebase=%d", plan.Timebase)
	}
	if plan.Duration != 15*90000 || !plan.Remainder.IsZero() {
		t.Fatalf("duration=%d rem=%s", plan.Duration, plan.Remainder)
	}
	if len(plan.Representations) != 2 {
		t.Fatalf("reps=%d", len(plan.Representations))
	}
	// Sorted by descending bandwidth.
	if plan.Representations[0].ID != "v-high" || plan.Representations[1].ID != "v-low" {
		t.Fatalf("order %s, %s", plan.Representations[0].ID, plan.Representations[1].ID)
	}
	for _, rep := range plan.Representations {
		if len(rep.Segments) != 3 {
			t.Fatalf("rep %s has %d segments", rep.ID, len(rep.Segments))
		}
		for i, seg := range rep.Segments {
			wantStart := int64(i) * 5 * 90000
			if seg.Start != wantStart || seg.Duration != 5*90000 {
				t.Fatalf("rep %s seg %d start=%d dur=%d", rep.ID, i, seg.Start, seg.Duration)
			}
			wantURI := rep.ID + "/seg-" + string(rune('1'+i)) + ".m4s"
			if seg.URI != wantURI {
				t.Fatalf("rep %s seg %d uri=%q want %q", rep.ID, i, seg.URI, wantURI)
			}
		}
	}
}

const dashTimeline = `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT9S">
  <Period id="p0">
    <AdaptationSet id="1" mimeType="video/mp4">
      <SegmentTemplate timescale="48000" media="v/seg-$Time$.m4s" startNumber="4">
        <SegmentTimeline>
          <S t="0" d="192000" r="1"/>
          <S d="96000"/>
          <S t="480000" d="96000"/>
        </SegmentTimeline>
      </SegmentTemplate>
      <Representation id="v0" bandwidth="1000"/>
    </AdaptationSet>
  </Period>
</MPD>
`

func TestParseDASHTimeline(t *testing.T) {
	plan, err := ParseDASH("tl.mpd", strings.NewReader(dashTimeline), Options{})
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	if plan.Timebase != 48000 {
		t.Fatalf("timebase=%d", plan.Timebase)
	}
	rep := plan.Representations[0]
	if len(rep.Segments) != 4 {
		t.Fatalf("segments=%d", len(rep.Segments))
	}
	wantStarts := []int64{0, 192000, 384000, 480000}
	wantNums := []string{"4", "5", "6", "7"}
	wantURIs := []string{"v/seg-0.m4s", "v/seg-192000.m4s", "v/seg-384000.m4s", "v/seg-480000.m4s"}
	for i, seg := range rep.Segments {
		if seg.Start != wantStarts[i] || seg.ID != wantNums[i] || seg.URI != wantURIs[i] {
			t.Fatalf("seg %d %+v", i, seg)
		}
	}
}

func TestParseDASHSharedTimebaseLCM(t *testing.T) {
	src := `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT4S">
  <Period id="p0">
    <AdaptationSet id="v" mimeType="video/mp4">
      <SegmentTemplate timescale="90000" duration="180000" media="v/$Number$.m4s"/>
      <Representation id="v0" bandwidth="2000"/>
    </AdaptationSet>
    <AdaptationSet id="a" mimeType="audio/mp4">
      <SegmentTemplate timescale="48000" duration="96000" media="a/$Number$.m4s"/>
      <Representation id="a0" bandwidth="128000"/>
    </AdaptationSet>
  </Period>
</MPD>`
	plan, err := ParseDASH("lcm.mpd", strings.NewReader(src), Options{})
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	if plan.Timebase != 720000 {
		t.Fatalf("shared base=%d want 720000", plan.Timebase)
	}
	for _, rep := range plan.Representations {
		for _, seg := range rep.Segments {
			if seg.Start%720000 != 0 && seg.Start != 4*720000 {
				// 2s and 3s boundaries at 720000: 1440000 and 2160000.
				if seg.Start != 1440000 && seg.Start != 2160000 {
					t.Fatalf("rep %s seg %s start=%d not aligned", rep.ID, seg.ID, seg.Start)
				}
			}
		}
	}
	// The two representations share the same [0, 4s) window.
	for _, rep := range plan.Representations {
		last := rep.Segments[len(rep.Segments)-1]
		if last.End() != 4*720000 {
			t.Fatalf("rep %s ends at %d, want %d", rep.ID, last.End(), 4*720000)
		}
	}
}

func TestParseDASHRejectsMissingDuration(t *testing.T) {
	src := `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static">
  <Period id="p0">
    <AdaptationSet id="v" mimeType="video/mp4">
      <SegmentTemplate timescale="90000" media="v/$Number$.m4s"/>
      <Representation id="v0" bandwidth="2000"/>
    </AdaptationSet>
  </Period>
</MPD>`
	_, err := ParseDASH("nodur.mpd", strings.NewReader(src), Options{})
	f, ok := model.AsFault(err)
	if !ok {
		t.Fatalf("want fault, got %v", err)
	}
	if f.Code != model.CodeManifestMissingField {
		t.Fatalf("code=%s msg=%s", f.Code, f.Message)
	}
}

func TestParseDASHRejectsForcedIndivisibleTimebase(t *testing.T) {
	_, err := ParseDASH("manifest.mpd", strings.NewReader(dashSample), Options{Timebase: 44100})
	f, ok := model.AsFault(err)
	if !ok {
		t.Fatalf("want fault, got %v", err)
	}
	if f.Code != model.CodeManifestTimebase {
		t.Fatalf("code=%s msg=%s", f.Code, f.Message)
	}
}

func TestParseDASHDurationParsing(t *testing.T) {
	cases := map[string]model.Rational{
		"PT6.006S":     {Num: 3003, Den: 500},
		"PT0H1M0.5S":   {Num: 121, Den: 2},
		"PT1H":         {Num: 3600, Den: 1},
		"PT0S":         {Num: 0, Den: 1},
		"P0Y0M0DT2M3S": {Num: 123, Den: 1},
	}
	for in, want := range cases {
		got, ok, err := parseMPDDuration(in)
		if err != nil || !ok {
			t.Fatalf("%q: ok=%v err=%v", in, ok, err)
		}
		if got != want {
			t.Fatalf("%q = %+v want %+v", in, got, want)
		}
	}
	for _, bad := range []string{"6S", "PT", "PT1X"} {
		if _, _, err := parseMPDDuration(bad); err == nil {
			t.Fatalf("%q should fail", bad)
		}
	}
}

func TestParseDASHRepeatToEnd(t *testing.T) {
	src := `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT10S">
  <Period id="p0" duration="PT10S">
    <AdaptationSet id="v" mimeType="video/mp4">
      <SegmentTemplate timescale="1000" media="v/$Number$.m4s" startNumber="1">
        <SegmentTimeline><S t="0" d="2000" r="-1"/></SegmentTimeline>
      </SegmentTemplate>
      <Representation id="v0" bandwidth="100"/>
    </AdaptationSet>
  </Period>
</MPD>`
	plan, err := ParseDASH("repeat.mpd", strings.NewReader(src), Options{})
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	if got := len(plan.Representations[0].Segments); got != 5 {
		t.Fatalf("segments=%d want 5", got)
	}
}
