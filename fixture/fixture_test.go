package fixture

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"archivecheck/manifest"
	rtpingest "archivecheck/rtp"
)

func TestBuildNormalFixtureIsParseable(t *testing.T) {
	f := Build(CaseNormal, 17)
	dash, err := manifest.ParseDASH([]byte(f.DASH), "manifest.mpd")
	if err != nil {
		t.Fatal(err)
	}
	hls, err := manifest.ParseHLS([]byte(f.HLS), "index.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	if len(dash.Representations) != 2 || len(hls.Segments) != 3 || len(f.Events) != 18 {
		t.Fatalf("unexpected fixture sizes: dash=%d hls=%d events=%d",
			len(dash.Representations), len(hls.Segments), len(f.Events))
	}
	var buf bytes.Buffer
	for _, e := range f.Events {
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if _, err := rtpingest.DecodeJSONL(&buf, "events.jsonl"); err != nil {
		t.Fatal(err)
	}
}

func TestBuildCorruptFixtureMutatesExpectedFiles(t *testing.T) {
	f := Build(CaseCorrupt, 17)
	if _, err := manifest.InspectFMP4(f.Files["dash/high/seg-2.m4s"], "dash-bad"); err == nil {
		t.Fatal("corrupt DASH fixture should fail fMP4 inspection")
	}
	if _, err := manifest.InspectFMP4(f.Files["hls/seg-1.m4s"], "hls-truncated"); err == nil {
		t.Fatal("truncated HLS fixture should fail fMP4 inspection")
	}
	dir := t.TempDir()
	if _, _, _, err := f.Write(filepath.Join(dir, "sample")); err != nil {
		t.Fatal(err)
	}
}
