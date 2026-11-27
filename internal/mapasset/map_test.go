package mapasset

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"replay/internal/fault"
	"replay/internal/media"
)

// buildAsset writes a generated MP4 into dir and probes it. contentKey
// selects the essence independently of the version label, so tests can
// model two versions that carry identical media.
func buildAsset(t *testing.T, dir, version, track string, salt byte, durationNS int64) Asset {
	t.Helper()
	return buildAssetContent(t, dir, version, track, version, salt, durationNS)
}

func buildAssetContent(t *testing.T, dir, version, track, contentKey string, salt byte, durationNS int64) Asset {
	t.Helper()
	// 1 kHz timescale keeps tick math obvious.
	assetBytes, err := media.Build(media.Asset{
		TrackID:       1,
		Timescale:     1000,
		DurationTicks: uint32(durationNS / 1_000_000),
		Essence:       media.EssenceFor(track, contentKey, salt, 64),
	})
	if err != nil {
		t.Fatalf("Build asset: %v", err)
	}
	path := filepath.Join(dir, track+"-"+version+".mp4")
	if err := os.WriteFile(path, assetBytes, 0o644); err != nil {
		t.Fatalf("Write asset: %v", err)
	}
	asset, err := ProbeFile(version, track, path, 0)
	if err != nil {
		t.Fatalf("ProbeFile: %v", err)
	}
	return asset
}

func TestProbeFileReadsMP4Structure(t *testing.T) {
	asset := buildAsset(t, t.TempDir(), "v1", "audio-main", 0, 500_000_000)
	if asset.Interval != (Interval{Start: 0, End: 500_000_000}) {
		t.Fatalf("interval=%v", asset.Interval)
	}
	if asset.Timescale != 1000 || asset.TrackID != 1 {
		t.Fatalf("timescale=%d track=%d", asset.Timescale, asset.TrackID)
	}
	if len(asset.Digest) != 64 {
		t.Fatalf("digest=%q", asset.Digest)
	}
	if asset.SampleCount != 1 {
		t.Fatalf("samples=%d", asset.SampleCount)
	}
}

func TestProbeFileReportsMissingAsset(t *testing.T) {
	_, err := ProbeFile("v1", "audio-main", filepath.Join(t.TempDir(), "nope.mp4"), 0)
	if err == nil || !fault.IsKind(err, fault.KindInput) {
		t.Fatalf("want input error, got %v", err)
	}
	if fe, _ := fault.As(err); fe.Version != "v1" || fe.Track != "audio-main" {
		t.Fatalf("context=%s", fe.Error())
	}
}

func TestSameContentOverlapMerges(t *testing.T) {
	dir := t.TempDir()
	left := buildAssetContent(t, dir, "v1", "audio-main", "shared", 0, 500_000_000)
	right := buildAssetContent(t, dir, "v2", "audio-main", "shared", 0, 500_000_000)
	// Shift v2 to overlap the second half of v1.
	right.Interval = Interval{Start: 250_000_000, End: 750_000_000}

	res := Map("audio-main", []Asset{left, right}, Interval{Start: 0, End: 750_000_000})
	if res.ConflictCount() != 0 {
		t.Fatalf("conflicts=%+v", res.Conflicts)
	}
	// The v1-only head and the shared middle collapse into one slot
	// whose contributors are both versions; the v2-only tail is its
	// own slot.
	if len(res.Slots) != 2 {
		t.Fatalf("slots=%+v", res.Slots)
	}
	head := res.Slots[0]
	if head.Interval != (Interval{Start: 0, End: 500_000_000}) {
		t.Fatalf("head interval=%v", head.Interval)
	}
	if len(head.MergedFrom) != 2 {
		t.Fatalf("merged_from=%v", head.MergedFrom)
	}
	if res.Merged != 1 {
		t.Fatalf("merged=%d", res.Merged)
	}
	// No time is missing: the two versions together cover [0,750ms).
	if res.MissingCount() != 0 {
		t.Fatalf("missing=%+v", res.Missing)
	}
}

func TestDifferentContentConflictsAndQuarantines(t *testing.T) {
	dir := t.TempDir()
	left := buildAssetContent(t, dir, "v1", "audio-main", "left", 1, 500_000_000)
	right := buildAssetContent(t, dir, "v2", "audio-main", "right", 2, 500_000_000)
	right.Interval = Interval{Start: 200_000_000, End: 700_000_000}

	res := Map("audio-main", []Asset{left, right}, Interval{Start: 0, End: 700_000_000})
	if res.ConflictCount() == 0 {
		t.Fatal("expected at least one conflict")
	}
	c := res.Conflicts[0]
	if c.LeftHash == "" || c.RightHash == "" || c.LeftHash == c.RightHash {
		t.Fatalf("conflict hashes=%q/%q", c.LeftHash, c.RightHash)
	}
	if !c.Quarantined {
		t.Fatal("conflict must be quarantined")
	}
	if res.Quarantined <= 0 {
		t.Fatalf("quarantined=%d", res.Quarantined)
	}
	// The conflicted segment must not appear in published slots.
	for _, s := range res.Slots {
		if s.Interval.Overlaps(c.Interval) {
			t.Fatalf("slot %v overlaps quarantined %v", s.Interval, c.Interval)
		}
	}
}

func TestMissingListsSourceVersion(t *testing.T) {
	dir := t.TempDir()
	short := buildAsset(t, dir, "v1", "audio-main", 0, 300_000_000)
	// A second version requested elsewhere covers nothing here, so the
	// missing record names both expected versions.
	res := Map("audio-main", []Asset{short}, Interval{Start: 0, End: 500_000_000})
	if res.MissingCount() == 0 {
		t.Fatal("expected missing range")
	}
	m := res.Missing[0]
	if m.Version != "v1" || m.Track != "audio-main" {
		t.Fatalf("missing must name the source version: %+v", m)
	}
	if m.Interval != (Interval{Start: 300_000_000, End: 500_000_000}) {
		t.Fatalf("missing interval=%v", m.Interval)
	}
}

func TestVersionThatLacksTrackIsMissing(t *testing.T) {
	dir := t.TempDir()
	onlyV1 := buildAsset(t, dir, "v1", "audio-main", 0, 400_000_000)
	// v2 supplies the video track but not audio, so it is absent here.
	res := Map("audio-main", []Asset{onlyV1}, Interval{Start: 0, End: 400_000_000})
	// No missing coverage, but the caller records v2's absence from the
	// asset list via MissingVersion.
	if res.MissingCount() != 0 {
		t.Fatalf("unexpected missing=%+v", res.Missing)
	}
	if got := MissingVersion("v2", "audio-main", Interval{Start: 0, End: 400_000_000}); got.Version != "v2" || got.Reason == "" {
		t.Fatalf("missing version record=%+v", got)
	}
}

func TestMapOutputIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	a := buildAssetContent(t, dir, "v1", "audio-main", "shared", 0, 400_000_000)
	b := buildAssetContent(t, dir, "v2", "audio-main", "shared", 0, 400_000_000)
	c := buildAssetContent(t, dir, "v3", "audio-main", "third", 5, 400_000_000)
	c.Interval = Interval{Start: 100_000_000, End: 500_000_000}
	want := Interval{Start: 0, End: 500_000_000}

	first := Map("audio-main", []Asset{a, b, c}, want)
	second := Map("audio-main", []Asset{c, b, a}, want)
	if len(first.Slots) != len(second.Slots) || len(first.Conflicts) != len(second.Conflicts) {
		t.Fatalf("shape changed: %+v vs %+v", first, second)
	}
	for i := range first.Slots {
		if !equalSlot(first.Slots[i], second.Slots[i]) {
			t.Fatalf("slot %d differs: %+v vs %+v", i, first.Slots[i], second.Slots[i])
		}
	}
	for i := range first.Conflicts {
		if first.Conflicts[i] != second.Conflicts[i] {
			t.Fatalf("conflict %d differs", i)
		}
	}
}

func TestIntervalHelpers(t *testing.T) {
	a := Interval{Start: 0, End: 10}
	b := Interval{Start: 5, End: 15}
	c := Interval{Start: 10, End: 20}
	if !a.Overlaps(b) || a.Overlaps(c) {
		t.Fatal("overlap semantics wrong")
	}
	if !(Interval{Start: 0, End: 20}).Contains(b) {
		t.Fatal("contains semantics wrong")
	}
	if (Interval{Start: 9, End: 2}).Duration() != 0 {
		t.Fatal("negative duration must clamp to zero")
	}
}

func equalSlot(a, b Slot) bool {
	if a.Track != b.Track || a.Interval != b.Interval || a.Version != b.Version || a.Digest != b.Digest {
		return false
	}
	if len(a.MergedFrom) != len(b.MergedFrom) {
		return false
	}
	for i := range a.MergedFrom {
		if a.MergedFrom[i] != b.MergedFrom[i] {
			return false
		}
	}
	return true
}

func TestProbeRejectsNonMP4(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bogus.mp4")
	if err := os.WriteFile(path, bytes.Repeat([]byte{0xde, 0xad}, 64), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ProbeFile("v1", "audio-main", path, 0)
	if err == nil || !fault.IsKind(err, fault.KindInput) {
		t.Fatalf("want input error, got %v", err)
	}
}
