package mediacore

import (
	"os"
	"path/filepath"
	"testing"
)

func remuxToTemp(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), name)
}

func TestRemuxNormalIsLossless(t *testing.T) {
	p := genFixtures(t, 17)
	c, err := DemuxFile(p.MP4, DefaultDemuxOptions())
	if err != nil {
		t.Fatal(err)
	}
	dst := remuxToTemp(t, "out.mp4")
	res, err := Remux(c, dst, RemuxOptions{Map: DefaultScanOptions().Map, Validate: true})
	if err != nil {
		t.Fatalf("Remux: %v", err)
	}
	if !res.SameMedia {
		t.Fatal("SameMedia must be true after successful validation")
	}
	if len(res.Tracks) != 2 {
		t.Fatalf("remux tracks = %d, want 2", len(res.Tracks))
	}
	for _, rt := range res.Tracks {
		if rt.SourceHash == "" || rt.SourceHash != rt.OutputHash {
			t.Fatalf("track %d hash mismatch src=%s out=%s", rt.SourceID, rt.SourceHash, rt.OutputHash)
		}
	}
	if st, err := os.Stat(dst); err != nil || st.Size() == 0 {
		t.Fatalf("output missing/empty: %v size=%d", err, func() int64 {
			if st != nil {
				return st.Size()
			}
			return 0
		}())
	}
}

func TestRemuxMOVIsLossless(t *testing.T) {
	p := genFixtures(t, 17)
	c, err := DemuxFile(p.MOV, DefaultDemuxOptions())
	if err != nil {
		t.Fatal(err)
	}
	dst := remuxToTemp(t, "out.mov")
	res, err := Remux(c, dst, RemuxOptions{Map: DefaultScanOptions().Map, Validate: true})
	if err != nil {
		t.Fatalf("Remux MOV: %v", err)
	}
	if !res.SameMedia {
		t.Fatal("MOV SameMedia must be true")
	}
}

func TestRemuxUnknownTrackRawEntryPreserved(t *testing.T) {
	p := genFixtures(t, 17)
	c, err := DemuxFile(p.Unknown, DefaultDemuxOptions())
	if err != nil {
		t.Fatal(err)
	}
	dst := remuxToTemp(t, "unknown.out.mp4")
	res, err := Remux(c, dst, RemuxOptions{Map: DefaultScanOptions().Map, Validate: true})
	if err != nil {
		t.Fatalf("Remux with unknown track: %v", err)
	}
	if len(res.Tracks) != 3 {
		t.Fatalf("tracks = %d, want 3", len(res.Tracks))
	}
	var preserved bool
	for _, rt := range res.Tracks {
		if rt.Preserved {
			preserved = true
			if rt.SourceHash != rt.OutputHash {
				t.Fatalf("preserved track %d payload changed", rt.SourceID)
			}
		}
	}
	if !preserved {
		t.Fatal("expected the unknown track to be reported preserved")
	}
}

func TestRemuxCollisionFailsBeforeOutput(t *testing.T) {
	p := genFixtures(t, 17)
	c, _ := DemuxFile(p.MP4, DefaultDemuxOptions())
	dst := remuxToTemp(t, "collision.mp4")
	cfg := TrackMapConfig{Rules: []TrackRule{
		{Kind: KindVideoTrack, SourceID: 1, TargetID: 5},
		{Kind: KindAudioTrack, SourceID: 2, TargetID: 5},
	}}
	_, err := Remux(c, dst, RemuxOptions{Map: cfg, Validate: false})
	if !IsKind(err, KindTrackMap) {
		t.Fatalf("expected track_map collision, got %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("output must not be created on pre-write collision, stat err=%v", err)
	}
}

func TestRemuxRangesRejectInvalid(t *testing.T) {
	p := genFixtures(t, 17)
	c, _ := DemuxFile(p.MP4, DefaultDemuxOptions())
	dst := remuxToTemp(t, "ranges.mp4")
	bad := map[uint32][]SampleRange{1: {{Start: 30, End: 100}}} // only 40 samples
	_, err := RemuxRanges(c, dst, bad, RemuxOptions{Map: DefaultScanOptions().Map})
	if !IsKind(err, KindIndex) {
		t.Fatalf("expected KindIndex for out-of-bounds range, got %v", err)
	}
	overlap := map[uint32][]SampleRange{1: {{Start: 0, End: 4}, {Start: 3, End: 6}}}
	_, err = RemuxRanges(c, dst, overlap, RemuxOptions{Map: DefaultScanOptions().Map})
	if !IsKind(err, KindIndex) {
		t.Fatalf("expected KindIndex for overlapping range, got %v", err)
	}
}
