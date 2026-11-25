// Command archivecheck is the terminal adapter for the resumable media
// archive kernel.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	apperrors "archivecheck/internal/errors"
	"archivecheck/internal/fixture"
	"archivecheck/internal/pipeline"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("archivecheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	generate := fs.String("generate", "", "generate deterministic fixtures in DIR")
	input := fs.String("input", "", "input manifest JSON")
	out := fs.String("out", "./tmp-out", "output directory")
	bitratesFlag := fs.String("bitrates", "", "comma-separated target bitrates")
	resumeAfter := fs.Int("resume-after", 0, "simulate interruption after N committed stage transitions")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if *generate != "" {
		paths, err := fixture.Generate(*generate)
		if err != nil {
			fmt.Fprintf(stderr, "archivecheck: generate fixtures failed: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "generated_fixtures dir=%s valid=%s truncated=%s bad_edit=%s\n",
			paths.Directory, paths.ValidInput, paths.Truncated, paths.BadEdit)
		return 0
	}
	if *input == "" {
		fmt.Fprintln(stderr, "archivecheck: either -generate DIR or -input FILE is required")
		return 2
	}
	if *resumeAfter < 0 {
		fmt.Fprintln(stderr, "archivecheck: -resume-after must be non-negative")
		return 2
	}
	bitrates, err := parseBitrates(*bitratesFlag)
	if err != nil {
		fmt.Fprintf(stderr, "archivecheck: invalid -bitrates: %v\n", err)
		return 2
	}

	res, err := pipeline.Run(pipeline.Config{
		InputPath:   *input,
		OutputDir:   *out,
		Bitrates:    bitrates,
		ResumeAfter: *resumeAfter,
		Log:         stdout,
	})
	if err != nil {
		printFailure(stderr, err)
		return apperrors.ExitCode(err)
	}
	printResult(stdout, res)
	return 0
}

func parseBitrates(s string) ([]int64, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out []int64
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		v, err := strconv.ParseInt(part, 10, 64)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("bitrate %q must be a positive integer", part)
		}
		out = append(out, v)
	}
	return out, nil
}

func printFailure(w io.Writer, err error) {
	kind := apperrors.KindOf(err)
	if kind == "" {
		kind = apperrors.KindIO
	}
	fmt.Fprintf(w, "status=failed error_kind=%s\n", kind)
	var ce *apperrors.Error
	if errors.As(err, &ce) {
		fmt.Fprintf(w, "error_stage=%s asset_id=%s path=%s bitrate=%d\n",
			ce.Stage, ce.AssetID, ce.Path, ce.Bitrate)
		if ce.IntervalIndex != nil {
			fmt.Fprintf(w, "interval_index=%d\n", *ce.IntervalIndex)
		}
	}
	fmt.Fprintf(w, "error=%v\n", err)
}

func printResult(w io.Writer, r *pipeline.Result) {
	m := r.Manifest
	fmt.Fprintf(w, "status=%s\n", m.Status)
	fmt.Fprintf(w, "stage_transition_count=%d # 阶段迁移数\n", m.TransitionCount)
	fmt.Fprintf(w, "journal_path=%s\n", r.JournalPath)
	for _, a := range m.Assets {
		for _, l := range a.Layers {
			fmt.Fprintf(w, "layer_cursor asset=%s bitrate=%d cursor=%d fragments=%d verified=%t\n",
				a.AssetID, l.Bitrate, l.Cursor, l.ProcessedFragments, l.Verified)
			for _, pos := range l.Recovery {
				fmt.Fprintf(w, "layer_recovery asset=%s bitrate=%d fragment=%d recovery_request_tick=%d recovery_actual_tick=%d delta_tick=%d # 请求/实际恢复位置\n",
					a.AssetID, pos.Bitrate, pos.FragmentIndex, pos.RequestTick, pos.ActualTick, pos.DeltaTicks)
			}
		}
	}
	fmt.Fprintf(w, "max_boundary_diff_tick=%d # 层间最大边界差，上限 1 tick\n", m.MaxBoundaryDiffTicks)
	fmt.Fprintf(w, "manifest_sha256=%s # 清单 SHA-256\n", r.ManifestSHA256)
	fmt.Fprintf(w, "artifact_sha256=%s # 产物集合 SHA-256\n", m.ArtifactSetSHA256)
	fmt.Fprintf(w, "artifact_set_sha256=%s\n", m.ArtifactSetSHA256)
	for _, a := range m.Artifacts {
		fmt.Fprintf(w, "artifact path=%s size=%d sha256=%s\n", a.Path, a.SizeBytes, a.SHA256)
	}
	fmt.Fprintf(w, "repeat_manifest_byte_diff=%d # 重复处理清单规范化字节差异\n", r.RepeatManifestByteDiff)
	fmt.Fprintf(w, "repeat_artifact_digest_diff=%d # 重复处理产物摘要差异\n", r.RepeatArtifactDigestDiff)
	fmt.Fprintf(w, "manifest_path=%s\n", r.ManifestPath)
}
