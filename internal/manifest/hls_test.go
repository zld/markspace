package manifest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"liveregress/internal/clock"
	"liveregress/internal/fault"
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const testMaster = `#EXTM3U
#EXT-X-VERSION:6
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aac",NAME="English",LANGUAGE="en",DEFAULT=YES,URI="audio/en.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=1280000,AVERAGE-BANDWIDTH=1100000,CODECS="avc1.4d401f,mp4a.40.2",RESOLUTION=640x360,AUDIO="aac"
video/low.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2560000,CODECS="avc1.4d401f,mp4a.40.2",RESOLUTION=1280x720,AUDIO="aac"
video/high.m3u8
`

func TestLoadMaster(t *testing.T) {
	root := t.TempDir()
	write(t, root, "master.m3u8", testMaster)
	write(t, root, "video/low.m3u8", "#EXTM3U\n")
	write(t, root, "video/high.m3u8", "#EXTM3U\n")
	write(t, root, "audio/en.m3u8", "#EXTM3U\n")

	m, err := LoadMaster(root, "master.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Variants) != 2 {
		t.Fatalf("variants = %d", len(m.Variants))
	}
	if m.Variants[0].Bandwidth != 1280000 || m.Variants[0].Path != "video/low.m3u8" {
		t.Fatalf("variant 0 = %+v", m.Variants[0])
	}
	if got := m.Variants[0].Codecs; len(got) != 2 || got[0] != "avc1.4d401f" {
		t.Fatalf("codecs = %v", got)
	}
	if len(m.Renditions) != 1 || m.Renditions[0].Path != "audio/en.m3u8" || !m.Renditions[0].Default {
		t.Fatalf("rendition = %+v", m.Renditions)
	}
}

func TestLoadMasterRejectsVariantEscape(t *testing.T) {
	root := t.TempDir()
	write(t, root, "master.m3u8", "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\n../../evil.m3u8\n")
	_, err := LoadMaster(root, "master.m3u8")
	if !errors.Is(err, fault.ErrPath) {
		t.Fatalf("want path fault, got %v", err)
	}
	if fe, ok := fault.As(err); !ok || fe.Path != "master.m3u8" {
		t.Fatalf("fault should name the referencing playlist: %+v", fe)
	}
}

func TestAttributesQuotedCommas(t *testing.T) {
	a := Attributes(`BANDWIDTH=1,CODECS="avc1.4d401f,mp4a.40.2",NAME="a,b",X=Y`)
	if a["CODECS"] != "avc1.4d401f,mp4a.40.2" {
		t.Fatalf("CODECS = %q", a["CODECS"])
	}
	if a["NAME"] != "a,b" {
		t.Fatalf("NAME = %q", a["NAME"])
	}
	if a["X"] != "Y" {
		t.Fatalf("X = %q", a["X"])
	}
}

const testMedia = `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-TARGETDURATION:4
#EXT-X-MEDIA-SEQUENCE:10
#EXT-X-DISCONTINUITY-SEQUENCE:2
#EXT-X-MAP:URI="init.mp4"
#EXTINF:3.333333,
seg1.m4s
#EXTINF:3.333333,
seg2.m4s
#EXT-X-DISCONTINUITY
#EXTINF:2.500000,
seg3.m4s
#EXT-X-ENDLIST
`

func TestLoadMediaExactTimeline(t *testing.T) {
	root := t.TempDir()
	write(t, root, "v0/index.m3u8", testMedia)
	for _, s := range []string{"init.mp4", "seg1.m4s", "seg2.m4s", "seg3.m4s"} {
		write(t, root, "v0/"+s, "0123456789")
	}
	p, err := LoadMedia(root, "v0/index.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Segments) != 3 {
		t.Fatalf("segments = %d", len(p.Segments))
	}
	if p.MediaSequence != 10 || p.Segments[0].Sequence != 10 || p.Segments[2].Sequence != 12 {
		t.Fatalf("sequences: %+v", p.Segments)
	}
	if !p.Segments[2].Discontinuity || p.Segments[2].DiscontinuitySeq != 3 {
		t.Fatalf("discontinuity not tracked: %+v", p.Segments[2])
	}
	if p.Segments[0].Init == nil || p.Segments[0].Init.Path != "v0/init.mp4" {
		t.Fatalf("init map = %+v", p.Segments[0].Init)
	}
	// 3 * 3333333/1000000 + 5/2 exactly.
	want := clock.MustRational(3333333, 1000000).MulInt(2).Add(clock.MustRational(5, 2))
	if p.DeclaredDuration().Compare(want) != 0 {
		t.Fatalf("declared = %v want %v", p.DeclaredDuration(), want)
	}
	bounds := p.Boundaries(90000)
	if bounds[0].Ticks != 0 || bounds[1].Ticks != 300000 || bounds[3].Ticks != 825000 {
		t.Fatalf("boundaries = %+v", bounds)
	}
}

func TestLoadMediaByteRanges(t *testing.T) {
	root := t.TempDir()
	write(t, root, "all.ts", string(make([]byte, 100)))
	write(t, root, "iframes.m3u8", `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-TARGETDURATION:4
#EXT-X-I-FRAMES-ONLY
#EXT-X-MAP:URI="all.ts",BYTERANGE="10@0"
#EXTINF:4.000000,
#EXT-X-BYTERANGE:20@10
all.ts
#EXTINF:4.000000,
#EXT-X-BYTERANGE:30
all.ts
#EXT-X-ENDLIST
`)
	p, err := LoadMedia(root, "iframes.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	if !p.IFramesOnly {
		t.Fatal("IFramesOnly not set")
	}
	if p.Segments[0].ByteRange.Offset != 10 || p.Segments[0].ByteRange.Length != 20 {
		t.Fatalf("range 0 = %+v", p.Segments[0].ByteRange)
	}
	if p.Segments[1].ByteRange.Offset != 30 || p.Segments[1].ByteRange.Length != 30 {
		t.Fatalf("implied range = %+v", p.Segments[1].ByteRange)
	}
	if p.Segments[1].Init == nil || p.Segments[1].Init.ByteRange == nil {
		t.Fatal("map byte range lost")
	}
}

func TestLoadMediaRejectsDeclaredRangePastEOF(t *testing.T) {
	root := t.TempDir()
	write(t, root, "all.ts", "0123456789")
	write(t, root, "x.m3u8", `#EXTM3U
#EXT-X-TARGETDURATION:1
#EXTINF:1.0,
#EXT-X-BYTERANGE:50@0
all.ts
`)
	_, err := LoadMedia(root, "x.m3u8")
	if !errors.Is(err, fault.ErrTruncated) {
		t.Fatalf("want truncate fault, got %v", err)
	}
}

func TestLoadMediaRejectsOverlappingRanges(t *testing.T) {
	root := t.TempDir()
	write(t, root, "all.ts", string(make([]byte, 100)))
	write(t, root, "x.m3u8", fmt.Sprintf(`#EXTM3U
#EXT-X-TARGETDURATION:1
#EXTINF:1.0,
#EXT-X-BYTERANGE:40@0
all.ts
#EXTINF:1.0,
#EXT-X-BYTERANGE:40@20
all.ts
`))
	_, err := LoadMedia(root, "x.m3u8")
	if !errors.Is(err, fault.ErrCorrupt) {
		t.Fatalf("want corrupt, got %v", err)
	}
}

func TestLoadMediaRejectsMaster(t *testing.T) {
	root := t.TempDir()
	write(t, root, "m.m3u8", testMaster)
	write(t, root, "video/low.m3u8", "#EXTM3U\n")
	write(t, root, "video/high.m3u8", "#EXTM3U\n")
	write(t, root, "audio/en.m3u8", "#EXTM3U\n")
	_, err := LoadMedia(root, "m.m3u8")
	if !errors.Is(err, fault.ErrFormat) {
		t.Fatalf("want format fault, got %v", err)
	}
}
