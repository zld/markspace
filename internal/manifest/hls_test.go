package manifest

import (
	"strings"
	"testing"
)

const mediaManifest = `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:4
#EXT-X-MEDIA-SEQUENCE:7
#EXT-X-PLAYLIST-TYPE:VOD
#EXTINF:3.003,
seg-00007.m4s
#EXTINF:3.003,
seg-00008.m4s
#EXTINF:2.994,
seg-00009.m4s
#EXT-X-ENDLIST
`

func TestParseHLSMediaTimelineIsExact(t *testing.T) {
	pl, err := ParseHLS("normal.m3u8", []byte(mediaManifest), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if pl.Kind != KindMedia || len(pl.Segments) != 3 || !pl.EndList {
		t.Fatalf("unexpected playlist: %+v", pl)
	}
	want := []struct{ start, dur int64 }{
		{0, 3_003_000_000},
		{3_003_000_000, 3_003_000_000},
		{6_006_000_000, 2_994_000_000},
	}
	for i, w := range want {
		if pl.Segments[i].StartNs != w.start || pl.Segments[i].DurationNs != w.dur {
			t.Fatalf("segment %d = %d+%d, want %d+%d", i, pl.Segments[i].StartNs, pl.Segments[i].DurationNs, w.start, w.dur)
		}
		if pl.Segments[i].Sequence != 7+int64(i) {
			t.Fatalf("segment %d sequence = %d", i, pl.Segments[i].Sequence)
		}
	}
	if pl.TotalNs != 9_000_000_000 {
		t.Fatalf("total = %d", pl.TotalNs)
	}
}

func TestParseHLSRejectsIllegalDurationsWithoutPartialIndex(t *testing.T) {
	cases := map[string]string{
		"missing uri":  "#EXTM3U\n#EXTINF:3.0,\n#EXT-X-ENDLIST\n",
		"no extinf":    "#EXTM3U\nseg1.m4s\n",
		"bad duration": "#EXTM3U\n#EXTINF:abc,\nseg1.m4s\n",
		"zero extinf":  "#EXTM3U\n#EXTINF:0,\nseg1.m4s\n#EXT-X-ENDLIST\n",
		"dangling inf": "#EXTM3U\n#EXTINF:3.0,\nseg1.m4s\n#EXTINF:2.0,\n",
		"remote uri":   "#EXTM3U\n#EXTINF:3.0,\nhttps://example.com/x.m4s\n#EXT-X-ENDLIST\n",
		"empty":        "#EXTM3U\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			pl, err := ParseHLS("bad.m3u8", []byte(body), Limits{})
			if err == nil {
				t.Fatalf("expected error, got %+v", pl)
			}
			if !strings.Contains(err.Error(), "bad.m3u8") {
				t.Fatalf("error lacks path: %v", err)
			}
		})
	}
}

func TestParseHLSRejectsEncrypted(t *testing.T) {
	body := "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"k.bin\"\n#EXTINF:3.0,\na.m4s\n#EXT-X-ENDLIST\n"
	_, err := ParseHLS("enc.m3u8", []byte(body), Limits{})
	if err == nil || !strings.Contains(err.Error(), "encrypted") {
		t.Fatalf("expected encryption rejection, got %v", err)
	}
}

func TestParseHLSMasterRecordsVariantsAndRenditions(t *testing.T) {
	body := `#EXTM3U
#EXT-X-VERSION:4
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="Main",LANGUAGE="en",DEFAULT=YES,URI="audio/en.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=800000,AVERAGE-BANDWIDTH=700000,RESOLUTION=1280x720,CODECS="avc1.64001f,mp4a.40.2",AUDIO="aud"
video/720p.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=400000,RESOLUTION=640x360
video/360p.m3u8
`
	pl, err := ParseHLS("master.m3u8", []byte(body), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if pl.Kind != KindMaster || len(pl.Variants) != 2 || len(pl.Renditions) != 1 {
		t.Fatalf("unexpected master: %+v", pl)
	}
	v := pl.Variants[0]
	if v.URI != "video/720p.m3u8" || v.Bandwidth != 800000 || v.Width != 1280 || v.Height != 720 || v.AudioGroup != "aud" {
		t.Fatalf("variant 0 = %+v", v)
	}
	if !pl.Renditions[0].Default || pl.Renditions[0].Language != "en" {
		t.Fatalf("rendition = %+v", pl.Renditions[0])
	}
	if pl.Variants[0].Line != 4 {
		t.Fatalf("variant line = %d, want 4", pl.Variants[0].Line)
	}
}

func TestParseHLSToleratesInformationalTags(t *testing.T) {
	body := `#EXTM3U
#EXT-X-VERSION:4
#EXT-X-TARGETDURATION:4
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-ALLOW-CACHE:YES
#EXT-X-PROGRAM-DATE-TIME:2024-01-01T00:00:00Z
#EXT-X-DISCONTINUITY
#EXTINF:3.003,
a.m4s
#EXT-X-ENDLIST
`
	pl, err := ParseHLS("info.m3u8", []byte(body), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pl.Segments) != 1 || !pl.Segments[0].Discontinuity {
		t.Fatalf("segment discontinuity not recorded: %+v", pl.Segments)
	}
}

func TestParseHLSRejectsMapAndEncrypted(t *testing.T) {
	for name, body := range map[string]string{
		"map":       "#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:3.0,\na.m4s\n#EXT-X-ENDLIST\n",
		"encrypted": "#EXTM3U\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"k\"\n#EXTINF:3.0,\na.m4s\n#EXT-X-ENDLIST\n",
	} {
		if _, err := ParseHLS(name+".m3u8", []byte(body), Limits{}); err == nil {
			t.Fatalf("%s should be rejected", name)
		}
	}
}

func TestParseHLSByteRange(t *testing.T) {
	body := "#EXTM3U\n#EXTINF:3.0,\n#EXT-X-BYTERANGE:100@0\na.m4s\n#EXTINF:3.0,\n#EXT-X-BYTERANGE:50\na.m4s\n#EXT-X-ENDLIST\n"
	pl, err := ParseHLS("br.m3u8", []byte(body), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if pl.Segments[0].Range.Offset != 0 || pl.Segments[1].Range.Offset != 100 {
		t.Fatalf("byte range offsets: %+v, %+v", pl.Segments[0].Range, pl.Segments[1].Range)
	}
}
