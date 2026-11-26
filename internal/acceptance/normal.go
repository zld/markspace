package acceptance

import (
	"fmt"
	"io"
	"math"
	"os"

	"mediarepair/internal/batch"
	"mediarepair/internal/cache"
	"mediarepair/internal/rollback"
	"mediarepair/internal/testfixture"
)

var platforms = []string{"darwin_arm64", "ios_arm64", "android_arm64"}

func init() {
	RegisterScenario("normal", runNormalDirect)
}

func runNormalDirect(opts Options, out io.Writer) int {
	code, _ := runNormalInRoot(opts.Root, opts.CheckpointFrames, true, out)
	return code
}

func runNormalInRoot(root string, blockSize int, publish bool, out io.Writer) (int, *normalArtifacts) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		fmt.Fprintf(out, "write_error=mkdir %q: %v\n", root, err)
		return ExitInputOrWrite, nil
	}
	if err := resetRunState(root); err != nil {
		fmt.Fprintf(out, "write_error=reset: %v\n", err)
		return ExitInputOrWrite, nil
	}
	fx, err := testfixture.CreateStandard(root)
	if err != nil {
		PrintError(out, err)
		return ExitForError(err), nil
	}
	c := cache.New()
	first, err := batch.Run(batch.Options{Root: root, BlockSize: blockSize, Cache: c})
	if err != nil {
		PrintError(out, err)
		return ExitForError(err), nil
	}
	second, err := batch.Run(batch.Options{Root: root, BlockSize: blockSize, Cache: c})
	if err != nil {
		PrintError(out, err)
		return ExitForError(err), nil
	}
	if mismatch := checkNormal(first, second); mismatch != "" {
		fmt.Fprintf(out, "result_mismatch=%s\n", mismatch)
		return ExitUsage, nil
	}

	art := &normalArtifacts{First: first, Second: second}
	if publish {
		for _, platform := range platforms {
			pm, perr := rollback.Publish(rollback.PublishOptions{
				Root: root, ArtifactsRoot: root, Platform: platform, Version: "v1.0.0",
				InputFingerprint: first.InputFingerprint, Index: first.Index, IndexSHA256: first.IndexSHA256,
			})
			if perr != nil {
				PrintError(out, perr)
				return ExitForError(perr), nil
			}
			art.Packages = append(art.Packages, pm)
		}
	}
	printNormal(out, fx.Manifest.FrameRate.String(), first, second, art.Packages)
	return ExitOK, art
}

type normalArtifacts struct {
	First    *batch.Result
	Second   *batch.Result
	Packages []*rollback.PackageManifest
}

func checkNormal(first, second *batch.Result) string {
	if len(first.Index.Records) != 12 {
		return fmt.Sprintf("index_entries=%d want 12", len(first.Index.Records))
	}
	if len(first.SegmentHashes) != 3 {
		return fmt.Sprintf("segments=%d want 3", len(first.SegmentHashes))
	}
	if first.Repair.RepairedCount != 2 {
		return fmt.Sprintf("repaired=%d want 2", first.Repair.RepairedCount)
	}
	if first.PixelMeanRGB != "93,95.5,108.5" {
		return "pixel_mean_rgb=" + first.PixelMeanRGB
	}
	if first.Audio == nil || math.Abs(first.Audio.PeakDBFS+6.02) > 0.01 {
		return fmt.Sprintf("audio_peak_dbfs=%v want -6.02", first.Audio)
	}
	if first.AlignMaxUS > 11.12 {
		return fmt.Sprintf("align_max_us=%.3f", first.AlignMaxUS)
	}
	if !validSHA(first.IndexSHA256) || first.IndexSHA256 != second.IndexSHA256 {
		return "index hash invalid or unstable"
	}
	if len(first.FrameHashes) != 12 {
		return "frame hash count"
	}
	for _, h := range first.FrameHashes {
		if !validSHA(h) {
			return "frame hash not sha256"
		}
	}
	for _, h := range first.SegmentHashes {
		if !validSHA(h) {
			return "segment hash not sha256"
		}
	}
	for i := range first.SegmentHashes {
		if first.SegmentHashes[i] != second.SegmentHashes[i] {
			return "segment hash unstable"
		}
	}
	return ""
}

func printNormal(out io.Writer, _ string, first, second *batch.Result, pkgs []*rollback.PackageManifest) {
	fmt.Fprintln(out, "scenario=normal status=ok")
	fmt.Fprintf(out, "frames=%d\n", len(first.FrameHashes))
	fmt.Fprintf(out, "repaired=%d\n", first.Repair.RepairedCount)
	fmt.Fprintf(out, "index_entries=%d\n", len(first.Index.Records))
	fmt.Fprintf(out, "segments=%d\n", len(first.SegmentHashes))
	fmt.Fprintf(out, "pixel_mean_rgb=%s\n", first.PixelMeanRGB)
	fmt.Fprintf(out, "audio_peak_dbfs=%.2f\n", first.Audio.PeakDBFS)
	fmt.Fprintf(out, "align_max_us=%.3f\n", first.AlignMaxUS)
	fmt.Fprintf(out, "input_sha256=%s\n", rawHash(first.InputFingerprint))
	fmt.Fprintf(out, "metadata_sha256=%s\n", rawHash(first.MetadataFP))
	fmt.Fprintf(out, "index_sha256=%s\n", first.IndexSHA256)
	for i, h := range first.FrameHashes {
		fmt.Fprintf(out, "frame_%04d_sha256=%s\n", i+1, h)
	}
	for i, h := range first.SegmentHashes {
		fmt.Fprintf(out, "segment_%03d_sha256=%s\n", i, h)
	}
	for _, pm := range pkgs {
		fmt.Fprintf(out, "package_%s_sha256=%s\n", pm.Platform, pm.PackageSHA256)
	}
	if len(pkgs) > 0 {
		fmt.Fprintf(out, "package_sha256=%s\n", pkgs[0].PackageSHA256)
	}
	fmt.Fprintf(out, "cache_decode_hits=%d cache_decode_misses=%d packaging_hits=%d packaging_misses=%d\n",
		second.CacheCounters.DecodeHits, second.CacheCounters.DecodeMisses,
		second.CacheCounters.PackagingHits, second.CacheCounters.PackagingMisses)
	fmt.Fprintln(out, "repeat_hash_match=true")
}
