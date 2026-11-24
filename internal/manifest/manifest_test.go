package manifest

import (
	"strings"
	"testing"

	"archivecheck/internal/media"
	"archivecheck/internal/merr"
)

const hlsValid = `#EXTM3U
#EXT-X-TARGETDURATION:2
#EXT-X-FIXTURE-PLAYLIST:v0,bandwidth=800000,width=160,height=90
#EXT-X-MEDIA-SEQUENCE:1
#EXTINF:2.000,
v0/seg-1.m4s
#EXTINF:2.000,
v0/seg-2.m4s
#EXT-X-FIXTURE-PLAYLIST:v1,bandwidth=420000,width=128,height=72
#EXT-X-MEDIA-SEQUENCE:1
#EXTINF:2.000,
v1/seg-1.m4s
#EXTINF:2.000,
v1/seg-2.m4s
`

func TestParseHLSValid(t *testing.T) {
	p, err := ParseHLS(strings.NewReader(hlsValid))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Format != FormatHLS || len(p.Tracks) != 2 {
		t.Fatalf("unexpected playlist: %+v", p)
	}
	if p.TargetDurationTick != 2 {
		t.Fatalf("target duration = %d", p.TargetDurationTick)
	}
	t0 := p.Tracks[0]
	if t0.Name != "v0" || t0.Kind != media.TrackVideo || len(t0.Refs) != 2 {
		t.Fatalf("bad track: %+v", t0)
	}
	if t0.Refs[1].Seq != 2 || t0.Refs[1].URI != "v0/seg-2.m4s" {
		t.Fatalf("bad second ref: %+v", t0.Refs[1])
	}
	// Duration must be an exact rational: 2.000 -> 2/1.
	r := t0.Refs[0].DurationRational()
	if r.Num().Int64() != 2 || r.Denom().Int64() != 1 {
		t.Fatalf("duration rational = %s", r.RatString())
	}
}

func TestParseHLSManifestErrors(t *testing.T) {
	cases := map[string]string{
		"missing duration": `#EXTM3U
#EXT-X-FIXTURE-PLAYLIST:v0,bandwidth=100000
#EXTINF:,
v0/seg-1.m4s
`,
		"illegal sequence": `#EXTM3U
#EXT-X-FIXTURE-PLAYLIST:v0
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:2.0,
v0/seg-1.m4s
`,
		"dangling extinf": `#EXTM3U
#EXT-X-FIXTURE-PLAYLIST:v0,bandwidth=100000
#EXTINF:2.0,
`,
		"remote uri": `#EXTM3U
#EXT-X-FIXTURE-PLAYLIST:v0,bandwidth=100000
#EXTINF:2.0,
https://cdn.example/v0/seg-1.m4s
`,
		"path traversal": `#EXTM3U
#EXT-X-FIXTURE-PLAYLIST:v0,bandwidth=100000
#EXTINF:2.0,
../escape/seg-1.m4s
`,
		"uri without extinf": `#EXTM3U
#EXT-X-FIXTURE-PLAYLIST:v0
v0/seg-1.m4s
`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseHLS(strings.NewReader(src))
			if err == nil {
				t.Fatal("expected locatable manifest error")
			}
			me, ok := merr.As(err)
			if !ok || me.Kind != merr.KindManifest || me.Location == "" {
				t.Fatalf("error not a locatable manifest error: %v", err)
			}
		})
	}
}

func TestParseHLSDuplicateReference(t *testing.T) {
	src := strings.ReplaceAll(hlsValid, "v0/seg-2.m4s", "v0/seg-1.m4s")
	_, err := ParseHLS(strings.NewReader(src))
	if err == nil || !strings.Contains(err.Error(), "duplicate segment reference") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
}

const dashValid = `<?xml version="1.0"?>
<MPD type="static" minBufferTime="PT2S">
  <Period id="P0">
    <AdaptationSet contentType="video">
      <Representation id="v0" bandwidth="800000" width="160" height="90">
        <SegmentList timescale="5" duration="10" startNumber="1">
          <SegmentURL media="v0/seg-1.m4s"/>
          <SegmentURL media="v0/seg-2.m4s"/>
        </SegmentList>
      </Representation>
      <Representation id="v1" bandwidth="420000" width="128" height="72">
        <SegmentList timescale="5" duration="10" startNumber="1">
          <SegmentURL media="v1/seg-1.m4s"/>
          <SegmentURL media="v1/seg-2.m4s"/>
        </SegmentList>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>`

func TestParseDASHValid(t *testing.T) {
	p, err := ParseDASH(strings.NewReader(dashValid))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Format != FormatDASH || len(p.Tracks) != 2 {
		t.Fatalf("bad playlist: %+v", p)
	}
	t0 := p.Tracks[0]
	if t0.Name != "v0" || t0.TargetBitrateBps != 800000 || t0.Width != 160 {
		t.Fatalf("bad representation: %+v", t0)
	}
	r := t0.Refs[0].DurationRational() // 10/5 = 2s
	if r.Num().Int64() != 2 || r.Denom().Int64() != 1 {
		t.Fatalf("duration = %s", r.RatString())
	}
	if t0.Refs[1].Seq != 2 {
		t.Fatalf("second seq = %d", t0.Refs[1].Seq)
	}
}

func TestParseDASHManifestErrors(t *testing.T) {
	base := func(body string) string {
		return `<MPD minBufferTime="PT2S"><Period><AdaptationSet contentType="video">` +
			body + `</AdaptationSet></Period></MPD>`
	}
	cases := map[string]string{
		"missing duration": base(`<Representation id="v" bandwidth="1000">
			<SegmentList timescale="5" duration="0"><SegmentURL media="v/a.m4s"/></SegmentList>
			</Representation>`),
		"missing timescale": base(`<Representation id="v" bandwidth="1000">
			<SegmentList timescale="0" duration="10">
			<SegmentURL media="v/a.m4s"/></SegmentList></Representation>`),
		"missing bandwidth": base(`<Representation id="v">
			<SegmentList timescale="5" duration="10"><SegmentURL media="v/a.m4s"/></SegmentList>
			</Representation>`),
		"duplicate ref": base(`<Representation id="v" bandwidth="1000">
			<SegmentList timescale="5" duration="10">
			<SegmentURL media="v/a.m4s"/><SegmentURL media="v/a.m4s"/>
			</SegmentList></Representation>`),
		"remote ref": base(`<Representation id="v" bandwidth="1000">
			<SegmentList timescale="5" duration="10">
			<SegmentURL media="https://x/v/a.m4s"/></SegmentList></Representation>`),
		"bad buffer": `<MPD minBufferTime="2S"><Period><AdaptationSet>` +
			`<Representation id="v" bandwidth="1000"><SegmentList timescale="5" duration="10">
			<SegmentURL media="v/a.m4s"/></SegmentList></Representation>
			</AdaptationSet></Period></MPD>`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseDASH(strings.NewReader(src))
			if err == nil {
				t.Fatal("expected manifest error")
			}
			me, ok := merr.As(err)
			if !ok || me.Kind != merr.KindManifest {
				t.Fatalf("not a manifest error: %v", err)
			}
		})
	}
}
