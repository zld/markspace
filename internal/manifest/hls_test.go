package manifest_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediacore/internal/diagnostic"
	"mediacore/internal/fixture"
	"mediacore/internal/manifest"
)

func TestHLSDiscontinuityUsesContinuousTimelineAndOriginalSequence(t *testing.T) {
	root := t.TempDir()
	b, err := fixture.GenerateAll(filepath.Join(root, "fixtures"))
	if err != nil {
		t.Fatal(err)
	}
	h, err := manifest.ParseHLS(b.NormalHLS, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Entries) != 2 {
		t.Fatalf("entries=%d", len(h.Entries))
	}
	if h.Entries[0].OriginalSequence != 10 || h.Entries[1].OriginalSequence != 11 {
		t.Fatalf("sequences=%d,%d", h.Entries[0].OriginalSequence, h.Entries[1].OriginalSequence)
	}
	if !h.Entries[1].DiscontinuityBefore {
		t.Fatal("discontinuity not preserved")
	}
	if h.Entries[0].ProgramStartU != 0 || h.Entries[1].ProgramStartU != 40_000_000 {
		t.Fatalf("timeline reset or drift: %+v %+v", h.Entries[0], h.Entries[1])
	}
	if h.Entries[1].ProgramEndU != 80_000_000 {
		t.Fatalf("end=%d", h.Entries[1].ProgramEndU)
	}
}

func TestHLSDuplicateSequenceIsSuppressed(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "dup.m3u8")
	content := `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:100
#EXTINF:0.040,
a.m4s
#EXT-X-ORIGINAL-SEQUENCE:100
#EXTINF:0.040,
b.m4s
#EXTINF:0.040,
c.m4s
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := manifest.ParseHLS(path, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Entries) != 2 {
		t.Fatalf("entries=%d, want duplicate sequence suppressed and next sequence retained", len(h.Entries))
	}
	if h.Entries[0].MediaURI != "a.m4s" || h.Entries[1].MediaURI != "c.m4s" {
		t.Fatalf("unexpected entries: %+v", h.Entries)
	}
}

func TestHLSTimeRegressionIsLocated(t *testing.T) {
	root := t.TempDir()
	b, err := fixture.GenerateAll(filepath.Join(root, "fixtures"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = manifest.ParseHLS(b.RegressionHLS, root)
	if !errors.Is(err, diagnostic.ErrTimeRegression) {
		t.Fatalf("regression err=%v", err)
	}
	pos := diagnostic.PositionOf(err)
	if pos.EntryIndex != 1 || pos.ByteOffset <= 0 {
		t.Fatalf("bad regression location: %+v", pos)
	}
}

func TestHLSMergeIsIdempotentByStableKey(t *testing.T) {
	e := []manifest.Entry{{StableKey: "hls:p:1", OriginalSequence: 1}}
	got := manifest.MergeEntries(e, []manifest.Entry{
		{StableKey: "hls:p:1", OriginalSequence: 1},
		{StableKey: "hls:p:2", OriginalSequence: 2},
	})
	if len(got) != 2 {
		t.Fatalf("merged len=%d", len(got))
	}
}
