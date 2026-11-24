package manifest

import (
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/model"
)

const mediaPlaylist = `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:2
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:2.000,
seg-0.m4s
#EXT-X-DISCONTINUITY
#EXTINF:2.000,
seg-1.m4s
#EXTINF:2.000,
seg-2.m4s
#EXT-X-ENDLIST
`

func TestHLSMediaDiscontinuity(t *testing.T) {
	m, err := ParseHLS([]byte(mediaPlaylist), "index.m3u8", nil)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	segs := m.Representations[0].Segments
	if len(segs) != 3 {
		t.Fatalf("segments = %d", len(segs))
	}
	if !segs[1].Discontinuity || segs[0].Discontinuity {
		t.Fatalf("discontinuity flag misplaced: %+v", segs)
	}
	// Timeline advances across the discontinuity without resetting.
	if segs[1].Start != 2_000_000_000 || segs[2].Start != 4_000_000_000 {
		t.Fatalf("timeline not monotonic across discontinuity: %d %d",
			segs[1].Start, segs[2].Start)
	}
	if m.Duration != 6_000_000_000 {
		t.Fatalf("duration = %d", m.Duration)
	}
	ratio, _, _ := RoundtripConsistency(m)
	if ratio != 1.0 {
		t.Fatalf("roundtrip = %f", ratio)
	}
}

func TestHLSMasterAlignment(t *testing.T) {
	dir := t.TempDir()
	master := `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=2000000
hi/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=500000
lo/index.m3u8
`
	mk := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("hi/index.m3u8", mediaPlaylist)
	// Low variant: second segment starts 25 ms late relative to hi variant.
	low := `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:2.000,
l-0.m4s
#EXT-X-DISCONTINUITY
#EXTINF:2.025,
l-1.m4s
#EXTINF:1.975,
l-2.m4s
#EXT-X-ENDLIST
`
	mk("lo/index.m3u8", low)
	masterPath := filepath.Join(dir, "master.m3u8")
	if err := os.WriteFile(masterPath, []byte(master), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := ParseHLSFile(masterPath)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(m.Representations) != 2 {
		t.Fatalf("variants = %d", len(m.Representations))
	}
	if len(m.Misaligned) != 1 {
		t.Fatalf("expected one misalignment, got %+v", m.Misaligned)
	}
	ma := m.Misaligned[0]
	if ma.OffsetNS <= AlignmentThresholdNS {
		t.Fatalf("offset %d must exceed %d", ma.OffsetNS, AlignmentThresholdNS)
	}
}

func TestHLSMasterAligned(t *testing.T) {
	dir := t.TempDir()
	mk := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(p, []byte(body), 0o644)
	}
	off := func() string {
		// 5 ms jitter stays inside the 20 ms budget.
		return `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:2.005,
l-0.m4s
#EXTINF:1.995,
l-1.m4s
#EXTINF:2.000,
l-2.m4s
#EXT-X-ENDLIST
`
	}()
	mk("hi/index.m3u8", mediaPlaylist)
	mk("lo/index.m3u8", off)
	masterPath := filepath.Join(dir, "master.m3u8")
	_ = os.WriteFile(masterPath, []byte(`#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=2000000
hi/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=500000
lo/index.m3u8
`), 0o644)
	m, err := ParseHLSFile(masterPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Misaligned) != 0 {
		t.Fatalf("aligned variants must not be flagged: %+v", m.Misaligned)
	}
}

func TestHLSRejectsBadPlaylist(t *testing.T) {
	cases := map[string]model.ErrKind{
		"not a playlist\n": model.KindCorruptBox,
		"#EXTM3U\nseg.m4s\n":                  model.KindMissingDuration,
		"#EXTM3U\n#EXTINF:bad,\nseg.m4s\n":    model.KindFieldOutOfRange,
		"#EXTM3U\n#EXTINF:2.0000000001,\nx\n": model.KindFieldOutOfRange,
	}
	for body, kind := range cases {
		_, err := ParseHLS([]byte(body), "p.m3u8", nil)
		se, ok := model.AsSource(err)
		if !ok || se.Kind != kind {
			t.Errorf("body %q: want %s got %v", body, kind, err)
		}
	}
}

func TestHLSMissingFile(t *testing.T) {
	_, err := ParseHLSFile("nope/missing.m3u8")
	se, ok := model.AsSource(err)
	if !ok || se.Kind != model.KindManifestMissing {
		t.Fatalf("got %v", err)
	}
}

func TestHLSMasterMissingVariant(t *testing.T) {
	_, err := ParseHLS([]byte(`#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=1
gone.m3u8
`), "master.m3u8", func(uri string) ([]byte, error) {
		return nil, os.ErrNotExist
	})
	se, ok := model.AsSource(err)
	if !ok || se.Kind != model.KindManifestMissing {
		t.Fatalf("got %v", err)
	}
}
