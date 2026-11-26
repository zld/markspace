package manifest

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"continuity/internal/core"
)

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseHLSLineNumbers(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "index.m3u8", `#EXTM3U
#EXT-X-TARGETDURATION:2
#EXT-X-MEDIA-SEQUENCE:7
#EXTINF:2.000,
seg0.m4s
#EXTINF:1.800,
sub/seg1.m4s
`)
	pl, err := ParseHLS(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(pl.Segments) != 2 {
		t.Fatalf("segments=%d, want 2", len(pl.Segments))
	}
	s0, s1 := pl.Segments[0], pl.Segments[1]
	if s0.Line != 5 || s0.Index != 7 || s0.DurTicks != 180000 {
		t.Fatalf("seg0: %+v", s0)
	}
	if s1.Line != 7 || s1.Index != 8 || s1.DurTicks != 162000 {
		t.Fatalf("seg1: %+v", s1)
	}
	if s1.URI != filepath.Join("sub", "seg1.m4s") {
		t.Fatalf("uri=%q", s1.URI)
	}
}

func TestParseHLSRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"dotdot":  "#EXTM3U\n../outside.m4s\n",
		"abs":     "#EXTM3U\n/etc/passwd\n",
		"remote":  "#EXTM3U\nhttps://example.com/s.m4s\n",
		"proto":   "#EXTM3U\n//example.com/s.m4s\n",
		"nohead":  "seg0.m4s\n",
		"empty":   "#EXTM3U\n#EXT-X-ENDLIST\n",
	}
	for name, body := range cases {
		p := write(t, dir, name+".m3u8", body)
		if _, err := ParseHLS(p); err == nil {
			t.Fatalf("%s: expected error", name)
		} else {
			var ce *core.Error
			if !errors.As(err, &ce) {
				t.Fatalf("%s: not a core.Error: %v", name, err)
			}
			if ce.Stage != core.StageManifest {
				t.Fatalf("%s: stage=%s", name, ce.Stage)
			}
		}
	}
}

func TestParseDASH(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "index.mpd", `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT4S">
  <Period>
    <AdaptationSet>
      <Representation id="v1">
        <SegmentList timescale="90000" duration="180000" startNumber="3">
          <SegmentURL media="a/seg0.m4s"/>
          <SegmentURL media="a/seg1.m4s"/>
        </SegmentList>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>
`)
	pl, err := ParseDASH(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(pl.Segments) != 2 {
		t.Fatalf("segments=%d", len(pl.Segments))
	}
	s := pl.Segments[0]
	if s.Index != 3 || s.DurTicks != 180000 || s.Line != 7 {
		t.Fatalf("seg0: %+v", s)
	}
	if pl.Segments[1].Index != 4 {
		t.Fatalf("seg1 index=%d", pl.Segments[1].Index)
	}
}

func TestParseDASHRejectsLiveAndEscape(t *testing.T) {
	dir := t.TempDir()
	live := write(t, dir, "live.mpd", `<MPD type="dynamic"><Period><SegmentList><SegmentURL media="s.m4s"/></SegmentList></Period></MPD>`)
	if _, err := ParseDASH(live); err == nil {
		t.Fatal("expected error for dynamic MPD")
	}
	esc := write(t, dir, "esc.mpd", `<MPD type="static"><Period><SegmentList><SegmentURL media="../x.m4s"/></SegmentList></Period></MPD>`)
	_, err := ParseDASH(esc)
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Code != core.CodePathEscape {
		t.Fatalf("want path_escape, got %v", err)
	}
	if ce.Line <= 0 {
		t.Fatalf("line not set: %+v", ce)
	}
}
