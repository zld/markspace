package manifest

import (
	"os"
	"path/filepath"
	"testing"

	"mediacheck/internal/timeline"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseHLS(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "playlist.m3u8",
		"#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:7\n"+
			"#EXTINF:1.000000,\nseg0.m4s\n"+
			"#EXT-X-DISCONTINUITY\n"+
			"#EXTINF:1.500000,\nseg1.m4s\n#EXT-X-ENDLIST\n")
	events, err := ParseHLS(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	e0, e1 := events[0], events[1]
	if e0.Seq != 7 || e1.Seq != 8 {
		t.Fatalf("seqs = %d,%d; want 7,8", e0.Seq, e1.Seq)
	}
	if e0.RawTime != 0 || e0.RawDuration != 1_000_000 || e0.Timescale != hlsTimescale {
		t.Fatalf("e0 timing = %+v", e0)
	}
	if e1.RawTime != 1_000_000 || e1.RawDuration != 1_500_000 {
		t.Fatalf("e1 timing = %+v", e1)
	}
	if e0.Discontinuity || !e1.Discontinuity {
		t.Fatalf("discontinuity flags = %v,%v; want false,true", e0.Discontinuity, e1.Discontinuity)
	}
	if e1.SegmentPath != filepath.Join(dir, "seg1.m4s") || e1.Line == 0 {
		t.Fatalf("e1 source = %q line %d", e1.SegmentPath, e1.Line)
	}
}

func TestParseHLSErrors(t *testing.T) {
	cases := []struct {
		name    string
		content string
		kind    timeline.Kind
	}{
		{"missing extinf", "#EXTM3U\nseg0.m4s\n", timeline.KindMissingDuration},
		{"bad extinf", "#EXTM3U\n#EXTINF:abc,\nseg0.m4s\n", timeline.KindMissingDuration},
		{"zero extinf", "#EXTM3U\n#EXTINF:0,\nseg0.m4s\n", timeline.KindMissingDuration},
		{"bad sequence", "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:x\n#EXTINF:1,\ns.m4s\n", timeline.KindBadSequence},
		{"negative sequence", "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:-2\n#EXTINF:1,\ns.m4s\n", timeline.KindBadSequence},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := writeFile(t, t.TempDir(), "p.m3u8", tc.content)
			_, err := ParseHLS(p)
			te, ok := timeline.As(err)
			if !ok || te.Kind != tc.kind {
				t.Fatalf("got %v, want kind %s", err, tc.kind)
			}
			if te.Source == "" || te.Line == 0 {
				t.Fatalf("error not locatable: %+v", te)
			}
		})
	}
}
