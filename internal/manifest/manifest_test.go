package manifest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"streamcheck/internal/model"
)

func segs() []model.Segment {
	return []model.Segment{
		{Rate: 800000, Number: 0, DurationMS: 1000, Path: "seg_800000_0_1000.ts"},
		{Rate: 800000, Number: 1, DurationMS: 950, Path: "seg_800000_1_950.ts"},
		{Rate: 800000, Number: 2, DurationMS: 1000, Path: "seg_800000_2_1000.ts"},
	}
}

func TestBuildAndParseRoundTrip(t *testing.T) {
	data, err := Build(800000, segs())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"#EXTM3U\n",
		"#EXT-X-VERSION:3\n",
		"#EXT-X-TARGETDURATION:1\n",
		"#EXT-X-MEDIA-SEQUENCE:0\n",
		"#EXTINF:0.950,\n",
		"#EXT-X-ENDLIST\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("manifest missing %q:\n%s", want, text)
		}
	}
	parsed, err := ParseBytes(data, 800000)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(parsed.Segments) != 3 || parsed.DurationMS() != 2950 {
		t.Fatalf("unexpected parse result: %+v", parsed)
	}
	if !parsed.EndList {
		t.Fatal("ENDLIST not parsed")
	}
	if parsed.Segments[1].DurationMS != 950 {
		t.Fatalf("millisecond precision lost: %d", parsed.Segments[1].DurationMS)
	}
}

func TestBuildRejectsBadInput(t *testing.T) {
	cases := [][]model.Segment{
		nil,
		{{Rate: 1, Number: 0, DurationMS: 0, Path: "a_1_0_0.ts"}},
		{{Rate: 1, Number: 0, DurationMS: 1000, Path: "a_1_0_1000.ts"}, {Rate: 1, Number: 0, DurationMS: 1000, Path: "b_1_0_1000.ts"}},
		{{Rate: 1, Number: 0, DurationMS: 1000, Path: ".hidden"}},
	}
	for _, input := range cases {
		if _, err := Build(1, input); err == nil {
			t.Errorf("Build(%+v) succeeded, want error", input)
		}
	}
}

func TestParseRejectsMalformedPlaylists(t *testing.T) {
	cases := map[string]string{
		"no header":       "#EXTINF:1.000,\na.ts\n#EXT-X-ENDLIST\n",
		"no target":       "#EXTM3U\n#EXTINF:1.000,\na.ts\n#EXT-X-ENDLIST\n",
		"dangling extinf": "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXTINF:1.000,\n",
		"uri first":       "#EXTM3U\n#EXT-X-TARGETDURATION:1\na.ts\n",
		"path traversal":  "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXTINF:1,\n../a.ts\n",
		"empty":           "",
	}
	for name, body := range cases {
		if _, err := Parse(strings.NewReader(body), 1); err == nil {
			t.Errorf("%s: expected parse error", name)
		}
	}
}

func TestDurationFormatting(t *testing.T) {
	cases := map[int64]string{
		1000: "1.000",
		950:  "0.950",
		5:    "0.005",
		1250: "1.250",
	}
	for ms, want := range cases {
		if got := FormatDurationMS(ms); got != want {
			t.Errorf("FormatDurationMS(%d) = %q, want %q", ms, got, want)
		}
		back, err := ParseDurationMS(want)
		if err != nil || back != ms {
			t.Errorf("ParseDurationMS(%q) = %d, %v", want, back, err)
		}
	}
}

func TestNormalizeURIRejectsEscapes(t *testing.T) {
	for _, uri := range []string{"../a.ts", "/etc/a.ts", "a/b.ts", "", "..", "."} {
		if _, err := NormalizeURI(uri); err == nil {
			t.Errorf("%q accepted, want error", uri)
		}
	}
	if got, err := NormalizeURI("seg_1_0_1000.ts"); err != nil || got != "seg_1_0_1000.ts" {
		t.Fatalf("NormalizeURI: %q %v", got, err)
	}
}

func TestWriteManyAtomicCommitsAllOrNothing(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "1.m3u8")
	second := filepath.Join(dir, "2.m3u8")
	if err := os.WriteFile(first, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := WriteManyAtomic(ctx, map[string][]byte{first: []byte("new"), second: []byte("new")})
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	body, _ := os.ReadFile(first)
	if string(body) != "old" {
		t.Fatalf("existing manifest was clobbered: %q", body)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("staging files left behind: %v", entries)
	}

	if err := WriteManyAtomic(context.Background(), map[string][]byte{first: []byte("new"), second: []byte("new")}); err != nil {
		t.Fatalf("WriteManyAtomic: %v", err)
	}
	for _, path := range []string{first, second} {
		body, err := os.ReadFile(path)
		if err != nil || string(body) != "new" {
			t.Fatalf("%s: %q %v", path, body, err)
		}
	}
}
