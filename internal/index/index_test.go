package index_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"medfix/internal/index"
	"medfix/internal/timeline"
)

func buildAligned(t *testing.T, withConflict bool) *timeline.AlignmentResult {
	t.Helper()
	cfg := timeline.DefaultConfig()
	a := timeline.NewAligner(cfg)
	a.AddTrack(timeline.Track{ID: "v", Timescale: 90000, IsKeyframe: true})
	a.AddTrack(timeline.Track{ID: "m", Timescale: 1000})
	for i := 0; i < 12; i++ {
		e := timeline.Event{
			Kind: timeline.KindSample, TrackID: "v", SourceID: "src", Seq: i,
			RawTS: int64(i) * 3000, Keyframe: i%4 == 0,
			RawPayload: []byte{byte(i)}, ContentHash: hsh(string([]byte{byte(i)})),
			Offset: timeline.OffsetRange{Begin: int64(i * 32), End: int64(i*32 + 32)},
		}
		_ = a.AddEvent(e)
	}
	// Metadata track: chapter + subtitle, no keyframes.
	_ = a.AddEvent(timeline.Event{
		Kind: timeline.KindChapter, TrackID: "m", SourceID: "src", Seq: 100,
		RawTS: 500, RawPayload: []byte("ch"), ContentHash: hsh("ch"),
		Offset: timeline.OffsetRange{Begin: 900, End: 902},
	})
	if withConflict {
		cfg2 := timeline.DefaultConfig()
		cfg2.ToleranceTicks = 1
		a2 := timeline.NewAligner(cfg2)
		a2.AddTrack(timeline.Track{ID: "v", Timescale: 1000, IsKeyframe: true})
		a2.AddTrack(timeline.Track{ID: "m", Timescale: 1000})
		_ = a2.AddEvent(timeline.Event{
			Kind: timeline.KindSubtitle, TrackID: "v", Seq: 1, RawTS: 0,
			RawPayload: []byte("dup"), ContentHash: hsh("dup"),
			Offset: timeline.OffsetRange{Begin: 1, End: 4},
		})
		_ = a2.AddEvent(timeline.Event{
			Kind: timeline.KindSubtitle, TrackID: "m", Seq: 2, RawTS: 100,
			RawPayload: []byte("dup"), ContentHash: hsh("dup"),
			Offset: timeline.OffsetRange{Begin: 9, End: 12},
		})
		res, _ := a2.Align()
		return res
	}
	res, err := a.Align()
	if err != nil {
		t.Fatalf("align: %v", err)
	}
	return res
}

func hsh(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestBuildAndQueryHits(t *testing.T) {
	res := buildAligned(t, false)
	idx, err := index.Build(res, "fp-1234", "cfg-abc")
	if err != nil {
		t.Fatal(err)
	}
	if idx.Version != index.IndexVersion {
		t.Fatalf("version=%d", idx.Version)
	}
	hits, statuses, err := idx.Query(index.Query{KeyframeOnly: true, TrackID: "v"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 3 { // samples 0,4,8
		t.Fatalf("keyframe hits=%d want 3", len(hits))
	}
	// Full reverse-lookup chain on every hit.
	for _, h := range hits {
		if h.Provenance.Fingerprint != "fp-1234" ||
			h.Provenance.RepairRuleID == "" ||
			h.Provenance.IndexVersion != index.IndexVersion ||
			h.Provenance.OffsetEnd <= h.Provenance.OffsetBegin {
			t.Fatalf("broken provenance chain: %+v", h.Provenance)
		}
	}
	// Explicit no-keyframe status for the metadata track (unfiltered query;
	// the keyframe-only query above restricts to track "v").
	_, allStatuses, qerr := idx.Query(index.Query{})
	if qerr != nil {
		t.Fatal(qerr)
	}
	var found bool
	for _, s := range allStatuses {
		if s.TrackID == "m" && s.Reason != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing no-keyframe status: %+v", statuses)
	}
}

func TestNearestKeyframeAndRange(t *testing.T) {
	res := buildAligned(t, false)
	idx, _ := index.Build(res, "fp", "cfg")
	hit, ok, err := idx.NearestKeyframe("v", idx.Entries[6].Ticks)
	if err != nil || !ok {
		t.Fatalf("nearest keyframe: ok=%v err=%v", ok, err)
	}
	if !hit.Keyframe || hit.Ticks > idx.Entries[6].Ticks {
		t.Fatalf("nearest keyframe wrong: %+v", hit)
	}
	rangeHits, _, err := idx.Query(index.Query{FromTick: idx.Entries[2].Ticks, ToTick: idx.Entries[5].Ticks})
	if err != nil {
		t.Fatal(err)
	}
	if len(rangeHits) < 2 {
		t.Fatalf("range hits=%d", len(rangeHits))
	}
	for _, h := range rangeHits {
		if h.Ticks < idx.Entries[2].Ticks || h.Ticks > idx.Entries[5].Ticks {
			t.Fatal("range query returned out-of-window entry")
		}
	}
	meta := idx.MetadataEvents()
	if len(meta) != 1 || meta[0].Kind != string(timeline.KindChapter) {
		t.Fatalf("metadata query=%d", len(meta))
	}
}

func TestBuildRefusesConflicts(t *testing.T) {
	res := buildAligned(t, true)
	if _, err := index.Build(res, "fp", "cfg"); err == nil {
		t.Fatal("index build must refuse conflicting timeline")
	}
}

func TestSaveLoadRoundTripAndTruncation(t *testing.T) {
	res := buildAligned(t, false)
	idx, _ := index.Build(res, "fp", "cfg")
	dir := t.TempDir()
	path := filepath.Join(dir, "index.mfix")
	if err := idx.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := index.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SHA256 != idx.SHA256 || len(loaded.Entries) != len(idx.Entries) {
		t.Fatal("round-trip mismatch")
	}
	// Truncated index must produce a structured, offset-carrying error.
	raw, _ := os.ReadFile(path)
	cut := len(raw) - 40
	if err := os.WriteFile(filepath.Join(dir, "cut.mfix"), raw[:cut], 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = index.Load(filepath.Join(dir, "cut.mfix"))
	var te *timeline.Error
	if !errors.As(err, &te) || te.Code != timeline.CodeTruncatedIndex {
		t.Fatalf("want truncated-index error, got %v", err)
	}
	if te.Offset < 0 {
		t.Fatal("truncation error lost offset")
	}
	// Byte corruption must fail the frame checksum.
	bad := append([]byte(nil), raw...)
	bad[20] ^= 0xFF
	if err := os.WriteFile(filepath.Join(dir, "bad.mfix"), bad, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Load(filepath.Join(dir, "bad.mfix")); err == nil {
		t.Fatal("corrupt index must not load")
	}
}

func TestRebuildProducesStableHash(t *testing.T) {
	res := buildAligned(t, false)
	idx1, _ := index.Build(res, "fp", "cfg")
	idx2, _ := index.Build(res, "fp", "cfg")
	if idx1.SHA256 != idx2.SHA256 {
		t.Fatal("rebuild hash differs for identical input")
	}
	// Different config hash must change the index identity.
	idx3, _ := index.Build(res, "fp", "cfg-changed")
	if idx3.SHA256 == idx1.SHA256 {
		t.Fatal("config change must invalidate index hash")
	}
}

func TestKeyframeHitRateMetric(t *testing.T) {
	res := buildAligned(t, false)
	idx, _ := index.Build(res, "fp", "cfg")
	rate := idx.KeyframeHitRate()
	if rate <= 0 || rate > 1 {
		t.Fatalf("hit rate out of range: %f", rate)
	}
	if rate != 3.0/12.0 {
		t.Fatalf("hit rate=%f want 0.25", rate)
	}
}
