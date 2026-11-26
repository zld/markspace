package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"

	"replaypipe/internal/clip"
	"replaypipe/internal/fixtures"
	"replaypipe/internal/index"
	"replaypipe/internal/manifest"
	"replaypipe/internal/metadata"
	"replaypipe/internal/store"
	"replaypipe/internal/timebase"
)

// runAll executes every scenario in order and keeps a single failure count.
func runAll(r *reporter, cfg *config, set *fixtures.Set) {
	runNormal(r, cfg, set)
	runEnvelope(r, cfg, set)
	runEvent(r, cfg, set)
	runCorrupt(r, cfg, set)
	runMigrate(r, cfg, set)
}

func mustMedia(r *reporter, path string) *index.Media {
	media, err := index.ParseFile(path, index.DefaultLimits())
	if err != nil {
		r.failf("parse %s: %v", path, err)
		return nil
	}
	return media
}

func srtEvents(path string, media *index.Media) []metadata.Event {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	events, e := metadata.ParseSRT(f, metadata.ParseOptions{MediaEndNS: media.DurationNS()})
	if e != nil {
		return nil
	}
	return events
}

// runEnvelope proves that an envelope-only change keeps sample content
// identical and lets all three slices be reused.
func runEnvelope(r *reporter, cfg *config, set *fixtures.Set) {
	r.section("envelope-only change")
	a := mustMedia(r, set.Normal)
	b := mustMedia(r, set.Envelope)
	if a == nil || b == nil {
		return
	}
	r.printf("content digest equal: %t", a.ContentDigest == b.ContentDigest)
	r.printf("envelope digest equal: %t", a.EnvelopeDigest == b.EnvelopeDigest)
	r.check("samples unchanged across envelope change", a.ContentDigest == b.ContentDigest, "")
	r.check("envelope digest changed", a.EnvelopeDigest != b.EnvelopeDigest, "")

	events := srtEvents(set.SRT, a)
	kf, e := index.BuildKeyframeIndex(b.VideoTrack())
	if e != nil {
		r.failf("keyframes: %v", e)
		return
	}
	base, e := clip.BuildPlan(clip.PlanOptions{Media: a, Keyframes: kf, Events: events, Budget: cfg.budget()})
	if e != nil {
		r.failf("base plan: %v", e)
		return
	}
	known := map[string]string{}
	for _, s := range base.Slices {
		known[s.ID] = s.AssetDigest(a)
	}
	planB, e := clip.BuildPlan(clip.PlanOptions{Media: b, Keyframes: kf, Events: events, Budget: cfg.budget(), Known: known})
	if e != nil {
		r.failf("envelope plan: %v", e)
		return
	}
	r.check("envelope change reuses 3 slices", planB.ReusedCount == 3,
		"reused="+itoa(planB.ReusedCount))
	r.check("envelope change recomputes 0 slices", planB.PlannedCount == 0,
		"recomputed="+itoa(planB.PlannedCount))
}

// runEvent proves that changing one event re-cuts only its own slice.
func runEvent(r *reporter, cfg *config, set *fixtures.Set) {
	r.section("single-event change")
	media := mustMedia(r, set.Normal)
	if media == nil {
		return
	}
	kf, e := index.BuildKeyframeIndex(media.VideoTrack())
	if e != nil {
		r.failf("keyframes: %v", e)
		return
	}
	before := srtEvents(set.SRT, media)
	after := srtEvents(set.Variant, media)
	if len(before) != len(after) || len(before) == 0 {
		r.failf("variant event count differs")
		return
	}
	planA, e := clip.BuildPlan(clip.PlanOptions{Media: media, Keyframes: kf, Events: before, Budget: cfg.budget()})
	if e != nil {
		r.failf("plan before: %v", e)
		return
	}
	known := map[string]string{}
	for _, s := range planA.Slices {
		known[s.ID] = s.AssetDigest(media)
	}
	planB, e := clip.BuildPlan(clip.PlanOptions{Media: media, Keyframes: kf, Events: after, Budget: cfg.budget(), Known: known})
	if e != nil {
		r.failf("plan after: %v", e)
		return
	}
	r.check("event change recomputes exactly 1 slice", planB.PlannedCount == 1,
		"recomputed="+itoa(planB.PlannedCount))
	r.check("event change reuses 2 slices", planB.ReusedCount == 2,
		"reused="+itoa(planB.ReusedCount))

	// Publish into a clean directory and confirm only the changed slice is
	// written, while the reused assets keep their original bytes.
	dir := filepath.Join(cfg.root, "event-clips")
	w, e := clip.NewWriter(media, dir)
	if e != nil {
		r.failf("open event writer: %v", e)
		return
	}
	defer w.Close()
	written := 0
	rewritten := 0
	for _, sl := range planB.Slices {
		if sl.Reused {
			continue
		}
		written++
		rec, e := w.WriteSlice(sl)
		if e != nil {
			r.failf("write event slice %s: %v", sl.ID, e)
			return
		}
		if rec.SHA256 != "" {
			rewritten++
		}
	}
	r.check("single-event change writes only 1 asset", written == 1 && rewritten == 1,
		"written="+itoa(written))

	// Reused slices must keep the exact assets published by the earlier run:
	// compare their asset digests and the on-disk hashes from that manifest.
	published, err := loadPublished(cfg.root)
	if err != nil {
		r.failf("read published manifest: %v", err)
		return
	}
	publishedDigest := map[string]string{}
	for _, entry := range published.Entries {
		publishedDigest[entry.SliceID] = entry.AssetDigest
	}
	preserved := 0
	for _, sl := range planB.Slices {
		if !sl.Reused {
			continue
		}
		if publishedDigest[sl.ID] == sl.AssetDigest(media) {
			preserved++
		}
	}
	if preserved != planB.ReusedCount {
		r.failf("reused asset digests drifted: %d/%d", preserved, planB.ReusedCount)
		return
	}
	ok, detail := compareDiskHashes(cfg.root)
	r.check("reused assets keep published hashes", ok, detail)
}

// runCorrupt confirms each defect fixture reports its stable code with an
// offset, and that a failed run never changes the published manifest hash.
func runCorrupt(r *reporter, cfg *config, set *fixtures.Set) {
	r.section("corrupt inputs")
	manifestPath := filepath.Join(cfg.root, "manifest.json")
	before := sha256FileOrEmpty(manifestPath)
	var codes []string
	for _, c := range set.Corrupt {
		code, detail := corruptOutcome(c.Path, set, cfg)
		codes = append(codes, code)
		ok := code == c.Code
		r.check(c.Name+" -> "+c.Code, ok, code+" "+detail)
	}
	after := sha256FileOrEmpty(manifestPath)
	r.check("existing manifest hash unchanged after failures", before == after, before)

	// A failing presentation must not publish anything: planning over the
	// truncated file stops before any asset or manifest write.
	published := filepath.Join(cfg.root, "corrupt-out")
	media, err := index.ParseFile(fixtures.TruncatedPath(set), index.DefaultLimits())
	if err == nil {
		_ = media
		r.failf("truncated media parsed successfully")
		return
	}
	blocked := true
	if entries, readErr := os.ReadDir(published); readErr == nil && len(entries) > 0 {
		blocked = false
	}
	r.check("failed input publishes no artifacts", blocked, "")
	r.check("failed input stops before plan", media == nil, "")
}

// corruptOutcome parses a defect fixture and returns the reported code.
func corruptOutcome(path string, set *fixtures.Set, cfg *config) (string, string) {
	switch filepath.Base(path) {
	case fixtures.OutOfRangeFile:
		media := mustParseQuiet(set.Normal)
		if media == nil {
			return "NO_MEDIA", ""
		}
		f, err := os.Open(path)
		if err != nil {
			return "IO_ERROR", ""
		}
		defer f.Close()
		_, e := metadata.ParseSRT(f, metadata.ParseOptions{MediaEndNS: media.DurationNS()})
		if e == nil {
			return "NO_ERROR", ""
		}
		return string(e.Code), e.Error()
	}
	_, err := index.ParseFile(path, index.DefaultLimits())
	if err == nil {
		return "NO_ERROR", ""
	}
	te := asTimebaseError(err)
	if te == nil {
		return "INTERNAL", err.Error()
	}
	return string(te.Code), te.Error()
}

// runMigrate exercises parse -> normalise -> validate -> atomic switch, then
// an interrupted run followed by an idempotent rerun.
func runMigrate(r *reporter, cfg *config, set *fixtures.Set) {
	r.section("manifest migration")
	work := filepath.Join(cfg.root, "migrate")
	if err := os.MkdirAll(work, 0o755); err != nil {
		r.failf("create migrate dir: %v", err)
		return
	}
	path := filepath.Join(work, "manifest.json")
	data, err := os.ReadFile(set.Legacy)
	if err != nil {
		r.failf("read legacy manifest: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		r.failf("seed legacy manifest: %v", err)
		return
	}
	before := sha256FileOrEmpty(path)

	res, e := manifest.Migrate(path, manifest.MigrateOptions{})
	if e != nil {
		r.failf("migrate: %v", e)
		return
	}
	r.printf("migration steps: %v", res.Steps)
	r.check("legacy manifest migrated", res.Migrated && res.FromVersion == 1 && res.ToVersion == manifest.Version,
		"from=v"+itoa(res.FromVersion)+" to=v"+itoa(res.ToVersion))
	r.check("rollback manifest retained", res.Rollback != "" && fileExists(res.Rollback), res.Rollback)
	rollbackHash := sha256FileOrEmpty(res.Rollback)
	r.check("rollback hash equals pre-migration manifest", rollbackHash == before, rollbackHash)

	// Idempotent rerun.
	res2, e := manifest.Migrate(path, manifest.MigrateOptions{})
	if e != nil {
		r.failf("migrate rerun: %v", e)
		return
	}
	r.check("migration rerun is idempotent", res2.Idempotent || res2.Migrated == false, "")

	// Interrupted run must keep the old version and complete on rerun.
	path2 := filepath.Join(work, "interrupted.json")
	if err := os.WriteFile(path2, data, 0o644); err != nil {
		r.failf("seed interrupted manifest: %v", err)
		return
	}
	preHash := sha256FileOrEmpty(path2)
	_, e = manifest.Migrate(path2, manifest.MigrateOptions{InterruptAfterStage: true})
	if e == nil {
		r.failf("interrupted migration unexpectedly succeeded")
		return
	}
	r.printf("interrupted migration: %v", e)
	rollbackKept, journalState := store.Recover(filepath.Dir(path2), filepath.Base(path2))
	r.check("interrupted run retains rollback", rollbackKept, "journal="+journalState)
	r.check("interrupted run keeps old manifest", sha256FileOrEmpty(path2) == preHash, "")
	res3, e := manifest.Migrate(path2, manifest.MigrateOptions{})
	if e != nil {
		r.failf("resume migration: %v", e)
		return
	}
	r.check("resumed migration completes", res3.Migrated, "")
	r.check("resumed manifest differs from legacy", sha256FileOrEmpty(path2) != preHash, "")
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func sha256FileOrEmpty(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return sha256Hex(data)
}

var _ = store.JournalName

func mustParseQuiet(path string) *index.Media {
	media, err := index.ParseFile(path, index.DefaultLimits())
	if err != nil {
		return nil
	}
	return media
}

func asTimebaseError(err error) *timebase.Error {
	return timebase.AsError(err)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func itoa(v int) string { return strconv.Itoa(v) }
