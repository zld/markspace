package manifest

import (
	"strings"
	"testing"

	"mediapipe/internal/clock"
)

const hlsOK = `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:2
#EXT-X-MEDIA-SEQUENCE:7
#EXTINF:2.000000,
seg007.m4s
#EXTINF:2.000000,
seg008.m4s
#EXT-X-ENDLIST
`

func TestHLSOK(t *testing.T) {
	p, err := ParseHLS("t0", strings.NewReader(hlsOK))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Refs) != 2 || !p.Complete {
		t.Fatalf("refs=%d complete=%v", len(p.Refs), p.Complete)
	}
	if p.Refs[0].Index != 7 || p.Refs[1].Index != 8 {
		t.Fatalf("sequence wrong: %+v", p.Refs)
	}
	if !p.Refs[0].Interval.Abuts(p.Refs[1].Interval) {
		t.Fatal("segments must be gapless")
	}
	if p.Duration() != clock.FromSeconds(4) {
		t.Fatalf("duration %d", p.Duration())
	}
}

func TestHLSDiscontinuityMarksMissing(t *testing.T) {
	src := strings.Replace(hlsOK, "#EXTINF:2.000000,\nseg008.m4s",
		"#EXT-X-DISCONTINUITY\n#EXTINF:2.000000,\nseg008.m4s", 1)
	p, err := ParseHLS("t0", strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Refs) != 3 || !p.Refs[1].Missing {
		t.Fatalf("expected missing slot: %+v", p.Refs)
	}
	// Timeline must not shrink: total span still 6s.
	if p.Duration() != clock.FromSeconds(6) {
		t.Fatalf("timeline shortened to %d", p.Duration())
	}
	if p.Refs[2].Interval.Start != clock.FromSeconds(4) {
		t.Fatalf("tail not shifted past gap: %+v", p.Refs[2].Interval)
	}
}

func TestHLSRejectsBadInput(t *testing.T) {
	cases := map[string]string{
		"negative duration": "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:-1.0,\na.m4s\n",
		"zero duration":     "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:0,\na.m4s\n",
		"path escape":       "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:1,\n../etc/passwd\n",
		"absolute path":     "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:1,\n/tmp/x.m4s\n",
		"remote uri":        "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:1,\nhttps://evil/x.m4s\n",
		"duplicate segment": "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:1,\na.m4s\n#EXTINF:1,\na.m4s\n",
		"empty":             "#EXTM3U\n#EXT-X-ENDLIST\n",
	}
	for name, src := range cases {
		if _, err := ParseHLS("t0", strings.NewReader(src)); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}

const dashOK = `<MPD type="static"><Period><AdaptationSet><Representation id="v1">
<BaseURL>track0/</BaseURL>
<SegmentTemplate timescale="48000" media="seg$Number$.m4s" startNumber="0">
<SegmentTimeline>
<S t="0" d="96000" r="1"/>
</SegmentTimeline>
</SegmentTemplate>
</Representation></AdaptationSet></Period></MPD>`

func TestDASHOK(t *testing.T) {
	p, err := ParseDASH("t0", strings.NewReader(dashOK))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Refs) != 2 {
		t.Fatalf("refs=%d", len(p.Refs))
	}
	if p.Refs[0].URI != "track0/seg0.m4s" || p.Refs[1].URI != "track0/seg1.m4s" {
		t.Fatalf("URIs: %+v", p.Refs)
	}
	if p.Refs[0].Interval.Dur() != 96000 {
		t.Fatalf("dur=%d", p.Refs[0].Interval.Dur())
	}
}

func TestDASHMissingIndex(t *testing.T) {
	// Second S starts at t=288000 while the first ends at 96000: a 2-slot
	// hole must be marked missing, preserving the full timeline.
	src := strings.Replace(dashOK,
		`<S t="0" d="96000" r="1"/>`,
		`<S t="0" d="96000"/><S t="288000" d="96000"/>`, 1)
	p, err := ParseDASH("t0", strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	var missing, live int
	for _, r := range p.Refs {
		if r.Missing {
			missing++
		} else {
			live++
		}
	}
	if missing != 2 || live != 2 {
		t.Fatalf("missing=%d live=%d refs=%+v", missing, live, p.Refs)
	}
	if p.Duration() != 384000 {
		t.Fatalf("timeline shortened to %d", p.Duration())
	}
}

func TestDASHRejectsBadInput(t *testing.T) {
	cases := map[string]string{
		"zero duration":   `<S t="0" d="0"/>`,
		"negative repeat": `<S t="0" d="96000" r="-1"/>`,
		"backward time":   `<S t="96000" d="96000"/><S t="0" d="96000"/>`,
	}
	for name, tl := range cases {
		src := strings.Replace(dashOK, `<S t="0" d="96000" r="1"/>`, tl, 1)
		if _, err := ParseDASH("t0", strings.NewReader(src)); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
	esc := strings.Replace(dashOK, `media="seg$Number$.m4s"`, `media="../seg$Number$.m4s"`, 1)
	if _, err := ParseDASH("t0", strings.NewReader(esc)); err == nil {
		t.Fatal("path escape: expected error")
	}
}
