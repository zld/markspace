package manifest

import (
	"strings"
	"testing"
)

func TestParseHLSDisccontinuityAndIntegerTime(t *testing.T) {
	playlist := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-TARGETDURATION:2",
		"#EXTINF:2.000,",
		"a0.ts",
		"#EXTINF:1.500000000,",
		"a1.ts",
		"#EXT-X-DISCONTINUITY",
		"#EXTINF:2.250,",
		"a2.ts",
		"",
	}, "\n")

	m, err := ParseHLS([]byte(playlist), "index.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Segments) != 3 {
		t.Fatalf("segments=%d", len(m.Segments))
	}
	if m.Segments[1].StartNS != 2_000_000_000 || m.Segments[1].DurationNS != 1_500_000_000 {
		t.Fatalf("bad timing: %+v", m.Segments[1])
	}
	if !m.Segments[2].Discontinuity || m.Segments[2].StartNS != 3_500_000_000 {
		t.Fatalf("discontinuity/timeline wrong: %+v", m.Segments[2])
	}
	if got := m.Discontinuities; len(got) != 1 || got[0] != 2 {
		t.Fatalf("discontinuities=%v", got)
	}
}

func TestParseHLSMalformedHasSourceOffset(t *testing.T) {
	_, err := ParseHLS([]byte("#EXTM3U\n#EXTINF:not-a-number,\nx.ts\n"), "bad.m3u8")
	if err == nil {
		t.Fatal("expected parse error")
	}
	f, ok := err.(interface{ Unwrap() error })
	_ = f
	if !strings.Contains(err.Error(), "bad.m3u8") || !strings.Contains(err.Error(), "offset 8") {
		t.Fatalf("error lacks source location: %v", err)
	}
	_ = ok
}
