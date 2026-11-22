package segmenter

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"archivist/internal/container"
	"archivist/internal/model"
	"archivist/internal/timeline"
)

// buildAlignedFixture generates the two-video + audio asset and runs parsing,
// index building and alignment so segmenter tests start from real structures.
func buildAlignedFixture(t *testing.T, corruption string) (
	*container.Fixture, *container.ParsedFile, *timeline.AlignmentResult,
	[]Representation, []*model.Track,
) {
	t.Helper()
	spec := container.FixtureSpec{
		Name: "seg-asset", Seed: 17, Brand: "isom", MovieTS: 24000,
		Corruption: corruption, DriftSamples: 3,
		Tracks: []container.TrackSpec{
			{ID: 1, Kind: model.KindVideo, Timescale: 24000, SampleCount: 60,
				SampleDur: 4000, KeyEvery: 10, BitrateLabel: "low",
				Color: model.ColorMeta{Present: true, ColorType: "nclc", Primaries: 1, Transfer: 1, Matrix: 1}},
			{ID: 2, Kind: model.KindVideo, Timescale: 24000, SampleCount: 60,
				SampleDur: 4000, KeyEvery: 10, BitrateLabel: "high",
				Color: model.ColorMeta{Present: true, ColorType: "nclc", Primaries: 9, Transfer: 16, Matrix: 9}},
			{ID: 3, Kind: model.KindAudio, Timescale: 48000, SampleCount: 240,
				SampleDur: 1000, Channels: 2, SampleRate: 48000},
		},
	}
	fix, err := container.Generate(spec)
	if err != nil {
		t.Fatal(err)
	}
	pf, err := container.ParseContainer(fix.Data, container.ParseOptions{Data: fix.Data})
	if err != nil {
		t.Fatal(err)
	}
	tcs := buildTrackClocks(t, pf)
	align, err := timeline.AlignBoundaries(tcs, 6)
	if err != nil {
		t.Fatal(err)
	}
	reps := []Representation{
		{Label: "video-low", Bitrate: 800000, TrackID: 1, Clock: tcs[0].Clock},
		{Label: "video-high", Bitrate: 1600000, TrackID: 2, Clock: tcs[1].Clock},
	}
	return fix, pf, align, reps, pf.Tracks
}

func buildTrackClocks(t *testing.T, pf *container.ParsedFile) []timeline.TrackClock {
	t.Helper()
	var out []timeline.TrackClock
	for _, tr := range pf.Tracks {
		if tr.Kind != model.KindVideo {
			continue
		}
		c, err := timeline.NewClock(tr.Timescale)
		if err != nil {
			t.Fatal(err)
		}
		var keys []uint64
		for _, s := range tr.Samples {
			if s.IsKeyframe {
				keys = append(keys, s.DTS)
			}
		}
		out = append(out, timeline.TrackClock{Track: tr, Clock: c, KeyDTS: keys})
	}
	return out
}

func TestCutOnCommonKeyframesAndHash(t *testing.T) {
	fix, _, align, reps, tracks := buildAlignedFixture(t, container.CorruptNone)
	plan, err := Cut(tracks, reps, align, 2000, fix.Data)
	if err != nil {
		t.Fatalf("cut: %v", err)
	}
	if plan.BoundaryCount != 6 {
		t.Fatalf("boundaries = %d, want 6", plan.BoundaryCount)
	}
	for _, rep := range reps {
		segs := plan.Segments[rep.TrackID]
		if len(segs) != 6 {
			t.Fatalf("rep %s segments = %d, want 6", rep.Label, len(segs))
		}
		for i, s := range segs {
			if s.Hash == "" {
				t.Fatalf("segment %d missing hash", i)
			}
			if len(s.ByteRanges) == 0 {
				t.Fatalf("segment %d has no byte ranges", i)
			}
		}
	}
}

func TestHashStableAcrossRuns(t *testing.T) {
	fix, _, align, reps, tracks := buildAlignedFixture(t, container.CorruptNone)
	plan1, err := Cut(tracks, reps, align, 2000, fix.Data)
	if err != nil {
		t.Fatal(err)
	}
	// Re-cut from the same bytes; hashes must be byte-identical.
	plan2, err := Cut(tracks, reps, align, 2000, fix.Data)
	if err != nil {
		t.Fatal(err)
	}
	for id, segs1 := range plan1.Segments {
		segs2 := plan2.Segments[id]
		for i := range segs1 {
			if segs1[i].Hash != segs2[i].Hash {
				t.Fatalf("hash drift track %d segment %d", id, i)
			}
		}
	}
}

func TestManifestVerifyHashes(t *testing.T) {
	fix, _, align, reps, tracks := buildAlignedFixture(t, container.CorruptNone)
	plan, err := Cut(tracks, reps, align, 2000, fix.Data)
	if err != nil {
		t.Fatal(err)
	}
	m, err := BuildManifest("seg-asset", plan, reps, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	matched, total, err := VerifyHashes(m, fix.Data)
	if err != nil || matched != total || total != 12 {
		t.Fatalf("verify hashes matched=%d total=%d err=%v", matched, total, err)
	}
	// Re-building must yield the same deterministic digest.
	m2, _ := BuildManifest("seg-asset", plan, reps, nil, nil)
	if m.Digest != m2.Digest {
		t.Fatal("manifest digest not deterministic")
	}
}

func TestDriftBlocksManifestAndLeavesNoFile(t *testing.T) {
	_, _, align, reps, tracks := buildAlignedFixture(t, container.CorruptKeyframeDrift)
	if align.Aligned {
		t.Fatal("drift fixture must produce unaligned result")
	}
	dir := t.TempDir()
	if _, err := Cut(tracks, reps, align, 2000, nil); !errors.Is(err, model.ErrNoCommonKeyframe) {
		t.Fatalf("cut must refuse unaligned boundaries: %v", err)
	}
	err := Reject(dir, "keyframe-index:video-high")
	if !errors.Is(err, model.ErrManifestBlocked) {
		t.Fatalf("reject error kind wrong: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "manifest.json")); !os.IsNotExist(statErr) {
		t.Fatal("blocked publication must leave no manifest file")
	}
}

func TestAtomicPublish(t *testing.T) {
	fix, _, align, reps, tracks := buildAlignedFixture(t, container.CorruptNone)
	plan, err := Cut(tracks, reps, align, 2000, fix.Data)
	if err != nil {
		t.Fatal(err)
	}
	m, err := BuildManifest("seg-asset", plan, reps, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path, err := Publish(m, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("manifest not published: %v", err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temp staging file must not remain after publish")
	}
}
