package manifest

import (
	"strings"
	"testing"

	"mediakernel/internal/mediaerr"
)

const validMPD = `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" mediaPresentationDuration="PT4S">
  <Period>
    <AdaptationSet contentType="video">
      <SegmentTemplate timescale="90000" media="v$RepresentationID$-$Number$.m4s"
        initialization="v$RepresentationID$-init.mp4">
        <SegmentTimeline>
          <S t="0" d="90000" r="3"/>
        </SegmentTimeline>
      </SegmentTemplate>
      <Representation id="v0" bandwidth="800000" codecs="avc1.640028"/>
      <Representation id="v1" bandwidth="1500000" codecs="avc1.4d401f"/>
    </AdaptationSet>
    <AdaptationSet contentType="audio">
      <SegmentTemplate timescale="48000" media="a0-$Number$.m4s"
        initialization="a0-init.mp4">
        <SegmentTimeline>
          <S t="0" d="48000" r="3"/>
        </SegmentTimeline>
      </SegmentTemplate>
      <Representation id="a0" bandwidth="128000" codecs="mp4a.40.2"/>
    </AdaptationSet>
  </Period>
</MPD>`

func TestParseDASHHappyPath(t *testing.T) {
	doc, err := ParseDASH(strings.NewReader(validMPD))
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	if len(doc.Representations) != 3 {
		t.Fatalf("reps = %d, want 3", len(doc.Representations))
	}
	v0 := doc.Representations[0]
	if v0.ID != "v0" || len(v0.Segments) != 4 {
		t.Fatalf("v0 segments = %d", len(v0.Segments))
	}
	if v0.Segments[0].StartTick != 0 || v0.Segments[1].StartTick != 90000 {
		t.Fatalf("video grid wrong: %+v", v0.Segments[:2])
	}
	wantURL := "vv0-0.m4s"
	if v0.Segments[0].URL != wantURL {
		t.Fatalf("url = %q, want %q", v0.Segments[0].URL, wantURL)
	}
	a0 := doc.Representations[2]
	if a0.Segments[1].StartTick != 90000 {
		t.Fatalf("audio tick = %d, want 90000", a0.Segments[1].StartTick)
	}
	if a0.EndTick != 360000 || doc.MediaPresentationDuration != 360000 {
		t.Fatalf("end = %d dur = %d", a0.EndTick, doc.MediaPresentationDuration)
	}
	// ranks are stable and unique
	ranks := map[int]bool{}
	for _, r := range doc.Representations {
		if ranks[r.Rank] {
			t.Fatalf("duplicate rank %d", r.Rank)
		}
		ranks[r.Rank] = true
	}
}

func TestParseDASHErrors(t *testing.T) {
	cases := []struct {
		name string
		mpd  string
		code string
		cat  mediaerr.Category
	}{
		{"zero timescale", strings.Replace(validMPD, `timescale="90000"`, `timescale="0"`, 1),
			"zero_timescale", mediaerr.CatTimebase},
		{"segment out of range", strings.Replace(validMPD, `mediaPresentationDuration="PT4S"`,
			`mediaPresentationDuration="PT1S"`, 1), "segment_out_of_range", mediaerr.CatValidation},
		{"bad xml", "<MPD><", "xml", mediaerr.CatManifest},
		{"no adaptation set", `<MPD mediaPresentationDuration="PT1S"><Period/></MPD>`,
			"no_adaptation", mediaerr.CatManifest},
		{"unsupported codec", strings.Replace(validMPD, `codecs="avc1.640028"`,
			`codecs="vp09.00"`, 1), "codec", mediaerr.CatManifest},
		{"missing duration", strings.Replace(validMPD, ` mediaPresentationDuration="PT4S"`, "", 1),
			"duration", mediaerr.CatManifest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDASH(strings.NewReader(tc.mpd))
			me, ok := mediaerr.As(err)
			if !ok {
				t.Fatalf("expected structured error, got %v", err)
			}
			if me.Code != tc.code || me.Category != tc.cat {
				t.Fatalf("got %s/%s, want %s/%s", me.Category, me.Code, tc.cat, tc.code)
			}
		})
	}
}

func TestParseDASHDuplicateSegment(t *testing.T) {
	// A static media template makes every timeline entry resolve to the
	// same reference, which must be rejected as a duplicate.
	mpd := `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" mediaPresentationDuration="PT2S">
  <Period>
    <AdaptationSet contentType="video">
      <SegmentTemplate timescale="90000" media="same.m4s">
        <SegmentTimeline>
          <S t="0" d="90000" r="1"/>
        </SegmentTimeline>
      </SegmentTemplate>
      <Representation id="v0" bandwidth="800000" codecs="avc1.640028"/>
    </AdaptationSet>
  </Period>
</MPD>`
	_, err := ParseDASH(strings.NewReader(mpd))
	me, ok := mediaerr.As(err)
	if !ok || me.Code != "duplicate_segment" {
		t.Fatalf("got %v, want duplicate_segment", err)
	}
}

func TestParseDASHTooManyRepresentations(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<MPD mediaPresentationDuration="PT1S"><Period><AdaptationSet contentType="video">
		<SegmentTemplate timescale="90000" media="x-$Number$.m4s">
		<SegmentTimeline><S t="0" d="90000"/></SegmentTimeline></SegmentTemplate>`)
	for i := 0; i < mediaerr.MaxRepresentations+1; i++ {
		b.WriteString(`<Representation id="r` + itoa(i) + `" bandwidth="1" codecs="avc1"/>`)
	}
	b.WriteString(`</AdaptationSet></Period></MPD>`)
	_, err := ParseDASH(strings.NewReader(b.String()))
	me, ok := mediaerr.As(err)
	if !ok || me.Category != mediaerr.CatValidation {
		t.Fatalf("got %v, want validation cap error", err)
	}
}

const validHLS = `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:2
#EXT-X-MEDIA-SEQUENCE:10
#EXTINF:2.0,
seg10.ts
#EXTINF:2.0,
seg11.ts
#EXT-X-DISCONTINUITY
#EXTINF:2.0,
seg12.ts
#EXTINF:2.0,
seg13.ts
#EXT-X-ENDLIST`

func TestParseHLSHappyPath(t *testing.T) {
	doc, err := ParseHLS(strings.NewReader(validHLS))
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	if len(doc.Groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(doc.Groups))
	}
	if doc.Groups[0][0].Sequence != 10 || doc.Groups[1][0].Sequence != 12 {
		t.Fatalf("sequence split wrong: %+v", doc.Groups)
	}
	if doc.Groups[1][0].StartTick != 0 {
		t.Fatalf("discontinuity must restart local timeline, got %d", doc.Groups[1][0].StartTick)
	}
	if doc.TargetDurationTick != 180000 {
		t.Fatalf("target = %d", doc.TargetDurationTick)
	}
}

func TestParseHLSMissingReference(t *testing.T) {
	playlist := `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:1.0,
a0.ts
#EXTINF:1.0,
a1.ts
#EXTINF:1.0,
a4.ts
#EXT-X-ENDLIST`
	doc, err := ParseHLS(strings.NewReader(playlist))
	if err != nil {
		t.Fatalf("missing refs must be reported, not fatal: %v", err)
	}
	if len(doc.Errors) != 1 || doc.Errors[0].Code != "missing_ref" {
		t.Fatalf("errors = %+v", doc.Errors)
	}
	if len(doc.Isolated) != 1 || doc.Isolated[0].Reason != "missing_reference" {
		t.Fatalf("isolation = %+v", doc.Isolated)
	}
	if len(doc.Groups) != 2 {
		t.Fatalf("hole must split groups, got %d", len(doc.Groups))
	}
}

func TestParseHLSRollback(t *testing.T) {
	playlist := `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:5
#EXTINF:1.0,
b5.ts
#EXTINF:1.0,
b6.ts
#EXTINF:1.0,
b2.ts
#EXT-X-ENDLIST`
	doc, err := ParseHLS(strings.NewReader(playlist))
	if err != nil {
		t.Fatalf("rollback must be reported, not fatal: %v", err)
	}
	found := false
	for _, e := range doc.Errors {
		if e.Code == "rollback" && e.Category == mediaerr.CatDiscontinuity {
			found = true
		}
	}
	if !found {
		t.Fatalf("rollback error missing: %+v", doc.Errors)
	}
	// no segment may carry b2 inside the same contiguous group as b5/b6
	for _, g := range doc.Groups {
		for _, s := range g {
			if s.URL == "b2.ts" && s.Group == 0 {
				t.Fatal("rollback segment must not share a group with the past")
			}
		}
	}
}

func TestParseHLSErrors(t *testing.T) {
	cases := []struct {
		name string
		pl   string
		code string
	}{
		{"empty", "#EXTM3U\n", "empty_playlist"},
		{"segment without extinf", "#EXTM3U\nnaked.ts\n", "hls_duration"},
		{"duplicate ref", "#EXTM3U\n#EXTINF:1.0,\nx.ts\n#EXTINF:1.0,\nx.ts\n", "duplicate_segment"},
		{"encrypted", "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128\n", "encrypted"},
		{"bad extinf", "#EXTM3U\n#EXTINF:abc,\nx.ts\n", "hls_extinf"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseHLS(strings.NewReader(tc.pl))
			me, ok := mediaerr.As(err)
			if !ok || me.Code != tc.code {
				t.Fatalf("got %v, want code %s", err, tc.code)
			}
		})
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
