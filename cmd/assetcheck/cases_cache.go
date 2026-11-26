package main

import (
	"fmt"
	"io"
	"path/filepath"

	"assetcheck/internal/fixture"
	"assetcheck/internal/media"
	"assetcheck/internal/task"
)

func demoReplaceIdentical(out io.Writer, dir string, redeliver int) error {
	opts := fixture.DefaultOptions()
	source := filepath.Join(dir, "clip.mkv")
	work := filepath.Join(dir, "work")
	if err := fixture.WriteFile(source, opts); err != nil {
		return err
	}
	base, err := task.NewRunner(work).Process(demoInput(source, opts.AssetID, ""))
	if err != nil {
		return err
	}
	if err := fixture.ReplaceWithSameBytes(source, opts); err != nil {
		return err
	}
	results, err := deliverConcurrent(work, demoInput(source, opts.AssetID, ""), redeliver)
	if err != nil {
		return err
	}
	if err := verifyResults(results); err != nil {
		return err
	}
	cur := rebuiltResult(results)
	if cur == nil || !cur.IndexRebuilt {
		return media.SpecMismatch("INDEX_NOT_REBUILT", "same-path identity replacement did not rebuild structure index")
	}
	if !cur.CacheHit {
		return media.SpecMismatch("CONTENT_CACHE_MISS", "byte-identical replacement did not reuse SHA/version content cache")
	}
	if cur.ManifestSHA256 != base.ManifestSHA256 {
		return media.SpecMismatch("MANIFEST_CHANGED_AFTER_IDENTICAL_REPLACE", "byte-identical replacement changed manifest")
	}
	fmt.Fprintln(out, "index_rebuilt=true")
	fmt.Fprintln(out, "cache_hit=true")
	fmt.Fprintf(out, "baseline_sha256=%s\n", base.ManifestSHA256)
	printManifest(out, cur)
	return nil
}

func demoMetadataChange(out io.Writer, dir string, redeliver int) error {
	opts := fixture.DefaultOptions()
	source := filepath.Join(dir, "clip.mkv")
	work := filepath.Join(dir, "work")
	if err := fixture.WriteFile(source, opts); err != nil {
		return err
	}
	if _, err := task.NewRunner(work).Process(demoInput(source, opts.AssetID, "")); err != nil {
		return err
	}

	changed := opts
	changed.WritingApp = "assetcheck-writer-metadata-r2"
	if err := fixture.WriteFile(source, changed); err != nil {
		return err
	}
	results, err := deliverConcurrent(work, demoInput(source, changed.AssetID, ""), redeliver)
	if err != nil {
		return err
	}
	if err := verifyResults(results); err != nil {
		return err
	}
	cur := committedResult(results)
	if cur == nil || !cur.IndexRebuilt {
		return media.SpecMismatch("METADATA_INDEX_STALE", "metadata change did not rebuild structure index")
	}
	if cur.CacheHit {
		return media.SpecMismatch("METADATA_CACHE_STALE", "metadata change incorrectly reused old content cache")
	}
	fmt.Fprintln(out, "index_rebuilt=true")
	fmt.Fprintln(out, "cache_hit=false")
	fmt.Fprintf(out, "cache_invalidated=%t\n", cur.CacheInvalidated)
	printManifest(out, cur)
	return nil
}
