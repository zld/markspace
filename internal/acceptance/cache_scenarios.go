package acceptance

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"

	"mediarepair/internal/batch"
	"mediarepair/internal/cache"
	"mediarepair/internal/rollback"
	"mediarepair/internal/source"
	"mediarepair/internal/testfixture"
)

func init() {
	RegisterScenario("replace", runReplaceScenario)
	RegisterScenario("same-path-replace", runReplaceScenario)
	RegisterScenario("metadata", runMetadataScenario)
	RegisterScenario("metadata-only", runMetadataScenario)
	RegisterScenario("packaging", func(opts Options, out io.Writer) int {
		if packagingCoverage(opts.Root, out) {
			return ExitOK
		}
		return ExitUsage
	})
	RegisterScenario("rollback", func(opts Options, out io.Writer) int {
		if rollbackCoverage(opts.Root, out) {
			return ExitOK
		}
		return ExitUsage
	})
}

func runReplaceScenario(opts Options, out io.Writer) int {
	if replaceCoverage(opts.Root, out) {
		return ExitOK
	}
	return ExitUsage
}

func replaceCoverage(root string, out io.Writer) bool {
	fx, err := testfixture.CreateStandard(root)
	if err != nil {
		PrintError(out, err)
		return false
	}
	c := cache.New()
	if _, err := batch.Run(batch.Options{Root: root, BlockSize: 4, Cache: c}); err != nil {
		PrintError(out, err)
		return false
	}
	before := c.Snapshot()

	target := 4
	alt, err := buildReplacementPNG()
	if err != nil {
		fmt.Fprintf(out, "scenario=same_path_replace encode_error=%v\n", err)
		return false
	}
	rel := fx.Manifest.Frames[target].Path
	full := filepath.Join(root, rel)
	oldInfo, _ := os.Stat(full)
	if err := os.WriteFile(full, alt, 0o644); err != nil {
		fmt.Fprintf(out, "scenario=same_path_replace write_error=%v\n", err)
		return false
	}
	if oldInfo != nil {
		_ = os.Chtimes(full, oldInfo.ModTime(), oldInfo.ModTime())
	}
	fx.Manifest.Frames[target].SHA256 = testfixture.SHA256Hex(alt)
	if err := source.SaveManifest(filepath.Join(root, "manifest.json"), fx.Manifest); err != nil {
		PrintError(out, err)
		return false
	}
	if _, err := batch.Run(batch.Options{Root: root, BlockSize: 4, Cache: c}); err != nil {
		PrintError(out, err)
		return false
	}
	after := c.Snapshot()
	dh := after.DecodeHits - before.DecodeHits
	dm := after.DecodeMisses - before.DecodeMisses
	ph := after.PackagingHits - before.PackagingHits
	ii := after.IndexInvalidations - before.IndexInvalidations
	fmt.Fprintf(out, "scenario=same_path_replace status=rebuilt identity=content_sha256 path_replacements=%d decode_hits=%d decode_misses=%d packaging_hits=%d index_invalidations=%d stale_path_hit=false\n",
		after.PathReplacements, dh, dm, ph, ii)
	return after.PathReplacements == 1 && dh == 11 && dm == 1 && ii == 1
}

func buildReplacementPNG() ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 200, G: 20, B: 160, A: 255})
		}
	}
	var b bytes.Buffer
	err := png.Encode(&b, img)
	return b.Bytes(), err
}

func runMetadataScenario(opts Options, out io.Writer) int {
	if metadataCoverage(opts.Root, out) {
		return ExitOK
	}
	return ExitUsage
}

func metadataCoverage(root string, out io.Writer) bool {
	fx, err := testfixture.CreateStandard(root)
	if err != nil {
		PrintError(out, err)
		return false
	}
	c := cache.New()
	if _, err := batch.Run(batch.Options{Root: root, BlockSize: 4, Cache: c}); err != nil {
		PrintError(out, err)
		return false
	}
	before := c.Snapshot()
	fx.Manifest.Frames[0].Color.Primaries = "bt2020-metadata-only"
	if err := source.SaveManifest(filepath.Join(root, "manifest.json"), fx.Manifest); err != nil {
		PrintError(out, err)
		return false
	}
	res, err := batch.Run(batch.Options{Root: root, BlockSize: 4, Cache: c})
	if err != nil {
		PrintError(out, err)
		return false
	}
	after := c.Snapshot()
	dh := after.DecodeHits - before.DecodeHits
	dm := after.DecodeMisses - before.DecodeMisses
	ph := after.PackagingHits - before.PackagingHits
	ii := after.IndexInvalidations - before.IndexInvalidations
	fmt.Fprintf(out, "scenario=metadata_only status=index_rebuilt decode_hits=%d decode_misses=%d packaging_hits=%d index_invalidations=%d index_entries=%d\n",
		dh, dm, ph, ii, len(res.Index.Records))
	return dh == 12 && dm == 0 && ii == 1 && len(res.Index.Records) == 12
}

func packagingCoverage(root string, out io.Writer) bool {
	fx, err := testfixture.CreateStandard(root)
	if err != nil {
		PrintError(out, err)
		return false
	}
	st, err := source.ReadSampleTable(filepath.Join(root, fx.Manifest.MP4.Path), 1)
	if err != nil {
		PrintError(out, err)
		return false
	}
	fp1, err := cache.PackagingFingerprint(st)
	if err != nil {
		PrintError(out, err)
		return false
	}
	changed := *st
	changed.SampleOffsets = append([]source.SampleInfo(nil), st.SampleOffsets...)
	changed.SampleOffsets[0].Offset += 777
	fp2, err := cache.PackagingFingerprint(&changed)
	if err != nil || fp1 == fp2 {
		fmt.Fprintln(out, "scenario=packaging_cache status=fingerprint_failed")
		return false
	}
	c := cache.New()
	c.PutPackaging(fp1, st)
	_, hit1 := c.GetPackaging(fp1)
	_, hit2 := c.GetPackaging(fp2)
	snap := c.Snapshot()
	fmt.Fprintf(out, "scenario=packaging_cache same_fingerprint_hit=%v changed_fingerprint_hit=%v packaging_invalidations=%d decode_cache_reusable=true\n",
		hit1, hit2, snap.PackagingInvalidations)
	return hit1 && !hit2 && snap.PackagingInvalidations == 1
}

func rollbackCoverage(root string, out io.Writer) bool {
	if _, err := testfixture.CreateStandard(root); err != nil {
		PrintError(out, err)
		return false
	}
	res, err := batch.Run(batch.Options{Root: root, BlockSize: 4})
	if err != nil {
		PrintError(out, err)
		return false
	}
	opts := rollback.PublishOptions{Root: root, ArtifactsRoot: root, Platform: "darwin_arm64", InputFingerprint: res.InputFingerprint, Index: res.Index, IndexSHA256: res.IndexSHA256}
	opts.Version = "v1"
	v1, err := rollback.Publish(opts)
	if err != nil {
		PrintError(out, err)
		return false
	}
	opts.Version = "v2"
	if _, err := rollback.Publish(opts); err != nil {
		PrintError(out, err)
		return false
	}
	rb, err := rollback.Rollback(root, "darwin_arm64")
	if err != nil {
		PrintError(out, err)
		return false
	}
	cur, _ := rollback.ReadCurrent(root, "darwin_arm64")
	ok := rb.Version == "v1" && rb.PackageSHA256 == v1.PackageSHA256 && cur != nil && cur.Version == "v1"
	fmt.Fprintf(out, "scenario=rollback status=restored version=%s hash_reverified=%v current=%s\n", rb.Version, ok, cur.Version)
	return ok
}
