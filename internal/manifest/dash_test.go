package manifest

import (
	"testing"

	"mediacheck/internal/timeline"
)

const mpdHead = `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static"><Period><AdaptationSet>
<Representation id="v1">`

const mpdTail = `</Representation></AdaptationSet></Period></MPD>`

func TestParseDASH(t *testing.T) {
	p := writeFile(t, t.TempDir(), "manifest.mpd", mpdHead+
		`<SegmentTemplate timescale="90000" initialization="init.mp4" media="chunk$Number$.m4s" startNumber="0">
		   <SegmentTimeline><S t="0" d="90000" r="2"/></SegmentTimeline>
		 </SegmentTemplate>`+mpdTail)
	events, err := ParseDASH(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	for i, e := range events {
		if e.Representation != "v1" || e.Seq != int64(i) || e.Timescale != 90000 {
			t.Fatalf("event %d = %+v", i, e)
		}
		if e.RawTime != uint64(i)*90000 || e.RawDuration != 90000 {
			t.Fatalf("event %d raw time = %d dur = %d", i, e.RawTime, e.RawDuration)
		}
	}
}

func TestParseDASHErrors(t *testing.T) {
	cases := []struct {
		name string
		tmpl string
		kind timeline.Kind
	}{
		{"zero timescale", `<SegmentTemplate timescale="0" media="c$Number$.m4s">
		  <SegmentTimeline><S t="0" d="9"/></SegmentTimeline></SegmentTemplate>`, timeline.KindZeroTimescale},
		{"no timeline", `<SegmentTemplate timescale="9" media="c$Number$.m4s" duration="9"/>`, timeline.KindMissingDuration},
		{"zero duration", `<SegmentTemplate timescale="9" media="c$Number$.m4s">
		  <SegmentTimeline><S t="0" d="0"/></SegmentTimeline></SegmentTemplate>`, timeline.KindMissingDuration},
		{"no start time", `<SegmentTemplate timescale="9" media="c$Number$.m4s">
		  <SegmentTimeline><S d="9"/></SegmentTimeline></SegmentTemplate>`, timeline.KindMissingDuration},
		{"negative startNumber", `<SegmentTemplate timescale="9" media="c$Number$.m4s" startNumber="-1">
		  <SegmentTimeline><S t="0" d="9"/></SegmentTimeline></SegmentTemplate>`, timeline.KindBadSequence},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := writeFile(t, t.TempDir(), "m.mpd", mpdHead+tc.tmpl+mpdTail)
			_, err := ParseDASH(p)
			te, ok := timeline.As(err)
			if !ok || te.Kind != tc.kind {
				t.Fatalf("got %v, want kind %s", err, tc.kind)
			}
		})
	}
}
