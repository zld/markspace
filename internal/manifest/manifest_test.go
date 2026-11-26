package manifest

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"medtrace/internal/core"
)

const dashFixture = `<?xml version="1.0" encoding="UTF-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static"
     mediaPresentationDuration="PT12S" minBufferTime="PT2S">
  <Period id="p0">
    <AdaptationSet contentType="video" mimeType="video/mp4">
      <SegmentTemplate timescale="90000" duration="180000"
          initialization="video/$RepresentationID$/init.mp4"
          media="video/$RepresentationID$/seg-$Number%03d$.m4s" startNumber="1"/>
      <Representation id="v-low" bandwidth="300000" width="640" height="360" codecs="avc1.42c01e"/>
      <Representation id="v-high" bandwidth="1200000" width="1280" height="720" codecs="avc1.42c01f"/>
    </AdaptationSet>
    <AdaptationSet contentType="audio">
      <SegmentTemplate timescale="44100" duration="88200"
          initialization="audio/init.mp4" media="audio/seg-$Number$.m4s" startNumber="10"/>
      <Representation id="a1" bandwidth="128000" codecs="mp4a.40.2"/>
    </AdaptationSet>
  </Period>
</MPD>
`

func TestParseDASHRepresentations(t *testing.T) {
	pl, err := Parse("dash/stream.mpd", []byte(dashFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if pl.Kind != DASHKind {
		t.Fatalf("kind = %q", pl.Kind)
	}
	if len(pl.Representations) != 3 {
		t.Fatalf("representations = %d, want 3", len(pl.Representations))
	}
	low, ok := pl.RepByID("v-low")
	if !ok {
		t.Fatal("v-low missing")
	}
	if low.Init != "dash/video/v-low/init.mp4" {
		t.Errorf("init = %q", low.Init)
	}
	if low.Width != 640 || low.Height != 360 {
		t.Errorf("dimensions = %dx%d", low.Width, low.Height)
	}
	// 12s / 2s = 6 segments per video representation, audio ditto.
	var video, audio int
	for _, s := range pl.Segments {
		switch s.Representation {
		case "v-low", "v-high":
			video++
			if !strings.HasPrefix(s.URI, "dash/video/"+s.Representation+"/seg-0") {
				t.Errorf("unexpected video URI %q", s.URI)
			}
			if s.Duration != 2*core.Second {
				t.Errorf("segment duration = %s, want 2s", s.Duration)
			}
		case "a1":
			audio++
		}
	}
	if video != 12 {
		t.Errorf("video segments = %d, want 12", video)
	}
	if audio != 6 {
		t.Errorf("audio segments = %d, want 6", audio)
	}
	// Audio starts at sequence 10.
	found := false
	for _, s := range pl.Segments {
		if s.Representation == "a1" && s.URI == "dash/audio/seg-10.m4s" {
			found = true
		}
	}
	if !found {
		t.Error("audio startNumber not honoured")
	}
}

func TestParseDASHTimelineAndWidth(t *testing.T) {
	src := `<MPD mediaPresentationDuration="PT8S"><Period>
  <AdaptationSet contentType="video">
    <Representation id="v" bandwidth="1" width="2" height="2">
      <SegmentTemplate timescale="1000" initialization="v/init.mp4" media="v/$Time$.m4s">
        <SegmentTimeline>
          <S t="1000" d="2000" r="2"/>
          <S d="2000"/>
        </SegmentTimeline>
      </SegmentTemplate>
    </Representation>
  </AdaptationSet>
</Period></MPD>`
	pl, err := Parse("m.mpd", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"v/1000.m4s", "v/3000.m4s", "v/5000.m4s", "v/7000.m4s"}
	if len(pl.Segments) != len(want) {
		t.Fatalf("segments = %d, want %d", len(pl.Segments), len(want))
	}
	for i, s := range pl.Segments {
		if s.URI != want[i] {
			t.Errorf("segment %d = %q, want %q", i, s.URI, want[i])
		}
	}
}

func TestParseDASHErrorsCarryLine(t *testing.T) {
	cases := []struct {
		name string
		src  string
		line int
		want string
	}{
		{
			name: "bad timescale",
			src: `<MPD><Period><AdaptationSet>
<Representation id="v"><SegmentTemplate timescale="zero" media="a.m4s"/></Representation>
</AdaptationSet></Period></MPD>`,
			line: 2,
			want: "timescale",
		},
		{
			name: "escape",
			src: `<MPD><Period><AdaptationSet><Representation id="v">
<SegmentTemplate timescale="1" duration="1" media="../../etc/passwd"/></Representation></AdaptationSet></Period></MPD>`,
			line: 2,
			want: "escapes",
		},
		{
			name: "malformed xml",
			src:  "<MPD><Period></MPD>",
			line: 1,
			want: "xml",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse("bad.mpd", []byte(tc.src))
			if err == nil {
				t.Fatal("expected error")
			}
			f := core.AsFault(err, core.KindManifest)
			if !strings.Contains(f.Error(), tc.want) {
				t.Errorf("error %q missing %q", f.Error(), tc.want)
			}
			if f.Pos.File != "bad.mpd" {
				t.Errorf("file = %q", f.Pos.File)
			}
			if f.Pos.Line != tc.line {
				t.Errorf("line = %d, want %d (err: %v)", f.Pos.Line, tc.line, err)
			}
		})
	}
}

const hlsFixture = `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-TARGETDURATION:10
#EXT-X-MEDIA-SEQUENCE:100
#EXT-X-PROGRAM-DATE-TIME:2024-01-01T00:00:00.000Z
#EXTINF:9.009,
seg-100.m4s
#EXTINF:9.009,
#EXT-X-DISCONTINUITY
sub/seg-101.m4s
#EXTINF:5.5,
#EXT-X-PROGRAM-DATE-TIME:2024-01-01T01:00:00.5Z
sub/seg-102.m4s
`

func TestParseHLSDiscontinuity(t *testing.T) {
	pl, err := Parse("hls/media.m3u8", []byte(hlsFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(pl.Segments) != 3 {
		t.Fatalf("segments = %d", len(pl.Segments))
	}
	if pl.Segments[0].URI != "hls/seg-100.m4s" || pl.Segments[1].URI != "hls/sub/seg-101.m4s" {
		t.Errorf("URIs = %q", []string{pl.Segments[0].URI, pl.Segments[1].URI})
	}
	if !pl.Segments[1].Discontinuity {
		t.Error("discontinuity not recorded on second segment")
	}
	if pl.Segments[0].Discontinuity {
		t.Error("discontinuity wrongly on first segment")
	}
	if got, want := pl.Segments[0].Duration, core.Nanos(9_009_000_000); got != want {
		t.Errorf("duration = %d, want %d", got, want)
	}
	if pl.Segments[0].Sequence != 100 || pl.Segments[2].Sequence != 102 {
		t.Errorf("sequences = %d, %d", pl.Segments[0].Sequence, pl.Segments[2].Sequence)
	}
	if pl.Segments[2].AnchorUnixNanos != 1704070800_500_000_000 {
		t.Errorf("anchor = %d", pl.Segments[2].AnchorUnixNanos)
	}
}

func TestParseHLSRejectsMasterAndDangling(t *testing.T) {
	master := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nlow.m3u8\n"
	_, err := Parse("m.m3u8", []byte(master))
	if err == nil || core.KindOf(err) != core.KindManifest {
		t.Fatalf("master playlist accepted: %v", err)
	}
	dangling := "#EXTM3U\n#EXTINF:4,\n"
	if _, err := Parse("m.m3u8", []byte(dangling)); err == nil {
		t.Fatal("dangling #EXTINF accepted")
	}
	remote := "#EXTM3U\n#EXTINF:4,\nhttp://example.com/a.m4s\n"
	_, err = Parse("m.m3u8", []byte(remote))
	if err == nil || core.KindOf(err) != core.KindPath {
		t.Fatalf("remote reference kind = %v, want path", core.KindOf(err))
	}
}

func TestLoadRejectsEscape(t *testing.T) {
	root := t.TempDir()
	// A valid playlist whose segment escapes root.
	src := "#EXTM3U\n#EXTINF:4,\n../outside.m4s\n"
	if err := os.WriteFile(filepath.Join(root, "m.m3u8"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(root, "m.m3u8")
	if err == nil || core.KindOf(err) != core.KindPath {
		t.Fatalf("escape kind = %v, want path", core.KindOf(err))
	}
}

func TestAbs(t *testing.T) {
	root := t.TempDir()
	got, err := Abs(root, "a/b.m4s")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, root) {
		t.Errorf("abs %q not under %q", got, root)
	}
	if _, err := Abs(root, "../x"); err == nil {
		t.Error("expected escape error")
	}
}

func TestISODuration(t *testing.T) {
	cases := map[string]core.Nanos{
		"PT12S":      12 * core.Second,
		"PT1M30S":    90 * core.Second,
		"PT0.5S":     500 * core.Millisecond,
		"P1DT2H":     26 * 3600 * core.Second,
		"PT1H2M3.5S": (3600+120+3)*core.Second + 500*core.Millisecond,
		"P0D":        0,
	}
	for in, want := range cases {
		got, err := parseISODuration(in)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%s = %d, want %d", in, got, want)
		}
	}
	for _, bad := range []string{"", "12S", "P1Y", "P1M", "PTX", "PT1S1S"} {
		if _, err := parseISODuration(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestParseRejectsUnknownFormat(t *testing.T) {
	_, err := Parse("x.txt", []byte("hello world"))
	if err == nil {
		t.Fatal("expected error")
	}
	var f *core.Fault
	if !errors.As(err, &f) || f.Pos.Line != 1 {
		t.Fatalf("unexpected fault: %v", err)
	}
}
