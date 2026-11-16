package mpd

import (
	"strings"
	"testing"

	"dashgen/internal/manifest"
)

func minimalDoc() *manifest.MPD {
	return &manifest.MPD{
		Profiles:                  manifest.MainProfile,
		Type:                      "static",
		MediaPresentationDuration: "PT2S",
		MinBufferTime:             "PT1S",
		Periods: []manifest.Period{{
			ID: "P0",
			AdaptationSets: []manifest.AdaptationSet{{
				ID:          0,
				ContentType: "video",
				MimeType:    manifest.VideoMimeType,
				Representations: []manifest.Representation{{
					ID:        "r1",
					Bandwidth: 1000,
					SegmentTemplate: &manifest.SegmentTemplate{
						Timescale:   300,
						Media:       "seg-$RepresentationID$-$Number$.m4s",
						StartNumber: 1,
						SegmentTimeline: &manifest.SegmentTimeline{
							Entries: []manifest.TimelineEntry{{Time: ptr(0), Duration: 100, Repeat: 2}},
						},
					},
				}},
			}},
		}},
	}
}

func ptr(v int64) *int64 { return &v }

func TestParseRoundTrip(t *testing.T) {
	data, err := Marshal(minimalDoc())
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rep := doc.Periods[0].AdaptationSets[0].Representations[0]
	if rep.ID != "r1" || rep.Bandwidth != 1000 {
		t.Fatalf("unexpected rep: %+v", rep)
	}
	tl := rep.SegmentTemplate.SegmentTimeline
	if len(tl.Entries) != 1 || tl.Entries[0].Duration != 100 || tl.Entries[0].Repeat != 2 {
		t.Fatalf("timeline lost in round trip: %+v", tl.Entries)
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"not xml":       "this is not xml",
		"empty root":    `<?xml version="1.0"?><MPD xmlns="urn:mpeg:dash:schema:mpd:2011"></MPD>`,
		"empty set":     `<MPD xmlns="urn:mpeg:dash:schema:mpd:2011"><Period id="P0"></Period></MPD>`,
		"missing media": `<MPD xmlns="urn:mpeg:dash:schema:mpd:2011"><Period id="P0"><AdaptationSet id="0"><Representation id="r" bandwidth="1"><SegmentTemplate timescale="1"/></Representation></AdaptationSet></Period></MPD>`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(in)); err == nil {
				t.Fatalf("%s: expected parse error", name)
			} else if !strings.Contains(err.Error(), "mpd: parse") {
				t.Fatalf("%s: error lacks package prefix: %v", name, err)
			}
		})
	}
}
