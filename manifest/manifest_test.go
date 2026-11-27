package manifest

import (
	"strconv"
	"strings"
	"testing"
)

func TestParseHLSBasic(t *testing.T) {
	src := `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-TARGETDURATION:1
#EXT-X-MEDIA-SEQUENCE:3
#EXT-X-MAP:URI="init.mp4"
#EXTINF:1.0,
seg3.m4s
#EXTINF:1.0,
seg4.m4s
#EXT-X-ENDLIST
`
	m, err := ParseHLS(strings.NewReader(src), "progA", "assets/progA")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Format != "hls" || len(m.Tracks) != 1 {
		t.Fatalf("bad manifest: %+v", m)
	}
	tr := m.Tracks[0]
	if tr.Timescale != hlsTimescale || tr.InitURI != "init.mp4" {
		t.Fatalf("bad track: %+v", tr)
	}
	if len(tr.Segments) != 2 || tr.Segments[0].Sequence != 3 || tr.Segments[1].Sequence != 4 {
		t.Fatalf("bad segments: %+v", tr.Segments)
	}
	if tr.Segments[0].StartNS != 0 || tr.Segments[0].DurationNS != 1e9 ||
		tr.Segments[1].StartNS != 1e9 {
		t.Fatalf("bad timeline: %+v", tr.Segments)
	}
	if m.DurationNS != 2e9 {
		t.Fatalf("duration = %d", m.DurationNS)
	}
}

func TestParseHLSRejects(t *testing.T) {
	cases := map[string]string{
		"traversal":      "#EXTINF:1.0,\n../evil.m4s\n",
		"absolute":       "#EXTINF:1.0,\n/var/evil.m4s\n",
		"bad_duration":   "#EXTINF:0,\nseg.m4s\n",
		"nan_duration":   "#EXTINF:NaN,\nseg.m4s\n",
		"missing_extinf": "seg.m4s\n",
		"dangling":       "#EXTINF:1.0,\n",
		"encrypted":      "#EXT-X-KEY:METHOD=AES-128,URI=\"k.bin\"\n#EXTINF:1.0,\ns.m4s\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseHLS(strings.NewReader(src), "p", "d")
			if err == nil {
				t.Fatal("expected error")
			}
			me, ok := err.(*Error)
			if !ok {
				t.Fatalf("not *Error: %T", err)
			}
			if me.Line == 0 || me.Stage == "" || me.Code == "" || me.Asset == "" {
				t.Fatalf("incomplete error record: %+v", me)
			}
		})
	}
}

func TestParseDASHSegmentTimeline(t *testing.T) {
	src := `<MPD xmlns="urn:mpeg:dash:schema:mpd:2011">
  <Period id="0">
    <AdaptationSet contentType="video">
      <SegmentTemplate timescale="90000" initialization="v/init.mp4"
        media="v/$Number$.m4s" startNumber="1">
        <SegmentTimeline>
          <S t="0" d="90000" r="2"/>
        </SegmentTimeline>
      </SegmentTemplate>
    </AdaptationSet>
    <AdaptationSet contentType="audio">
      <SegmentTemplate timescale="48000" initialization="a/init.mp4"
        media="a/$Number$.m4s">
        <SegmentTimeline>
          <S t="0" d="48000"/>
        </SegmentTimeline>
      </SegmentTemplate>
    </AdaptationSet>
  </Period>
</MPD>`
	m, err := ParseDASH(strings.NewReader(src), "progB", "assets/progB")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(m.Tracks) != 2 {
		t.Fatalf("tracks = %d", len(m.Tracks))
	}
	v := m.Tracks[0]
	if v.Kind != KindVideo || len(v.Segments) != 3 {
		t.Fatalf("video track: %+v", v)
	}
	for i, s := range v.Segments {
		if s.Sequence != int64(i+1) || s.StartNS != int64(i)*1e9 || s.DurationNS != 1e9 {
			t.Fatalf("segment %d: %+v", i, s)
		}
		if s.URI != "v/"+strconv.Itoa(i+1)+".m4s" {
			t.Fatalf("uri = %q", s.URI)
		}
	}
	a := m.Tracks[1]
	if a.Timescale != 48000 || a.Segments[0].DurationNS != 1e9 {
		t.Fatalf("audio track: %+v", a)
	}
}

func TestParseDASHRejects(t *testing.T) {
	base := func(body string) string {
		return `<MPD xmlns="urn:mpeg:dash:schema:mpd:2011"><Period id="0">` +
			body + `</Period></MPD>`
	}
	cases := map[string]string{
		"zero_timescale": base(`<AdaptationSet contentType="video"><SegmentTemplate timescale="0"
			media="v/$Number$.m4s"><SegmentTimeline><S d="90000"/></SegmentTimeline>
			</SegmentTemplate></AdaptationSet>`),
		"bad_duration": base(`<AdaptationSet contentType="video"><SegmentTemplate timescale="90000"
			media="v/$Number$.m4s"><SegmentTimeline><S d="0"/></SegmentTimeline>
			</SegmentTemplate></AdaptationSet>`),
		"traversal": base(`<AdaptationSet contentType="video"><SegmentTemplate timescale="90000"
			media="../$Number$.m4s"><SegmentTimeline><S d="90000"/></SegmentTimeline>
			</SegmentTemplate></AdaptationSet>`),
		"malformed_xml": `<MPD><Period><`,
		"no_period":     `<MPD xmlns="x"></MPD>`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseDASH(strings.NewReader(src), "p", "d")
			if err == nil {
				t.Fatal("expected error")
			}
			me, ok := err.(*Error)
			if !ok {
				t.Fatalf("not *Error: %T %v", err, err)
			}
			if me.Code == "" || me.Stage == "" {
				t.Fatalf("incomplete record: %+v", me)
			}
			if name == "malformed_xml" && me.Line == 0 {
				t.Fatalf("syntax error missing line: %+v", me)
			}
		})
	}
}

func TestSafeURI(t *testing.T) {
	good := []string{"a/b.m4s", "v/1.m4s", "seg.m4s", "x/./y.mp4"}
	bad := []string{"", "../x", "a/../../b", "/abs/path", `C:\x`, "http://h/x", "data:abc"}
	for _, u := range good {
		if !SafeURI(u) {
			t.Errorf("expected safe: %q", u)
		}
	}
	for _, u := range bad {
		if SafeURI(u) {
			t.Errorf("expected unsafe: %q", u)
		}
	}
}

func TestScaleCheckedOverflow(t *testing.T) {
	if _, ok := scaleChecked(1<<62, 90000); ok {
		t.Fatal("expected overflow")
	}
	if v, ok := scaleChecked(90000, 90000); !ok || v != 1e9 {
		t.Fatalf("scale = %d ok=%v", v, ok)
	}
}
