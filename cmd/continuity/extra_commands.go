package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"continuity/internal/fixture"
	"continuity/internal/manifest"
	"continuity/internal/media"
	"continuity/internal/resume"
)

func runPlan(args []string) int {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	dir := fs.String("dir", "", "generated fixture directory")
	manifestPath := fs.String("manifest", "", "manifest JSON (default DIR/manifest-canonical.json)")
	budgetMS := fs.Int64("budget-ms", 2000, "processing budget override")
	maxBitrate := fs.Int("max-bitrate-bps", 2_400_000, "bitrate cap override")
	minCoverage := fs.Int("min-coverage-ppm", 999000, "coverage floor override")
	maxSkew := fs.Int("max-av-skew-ms", 20, "A/V skew limit override")
	if err := fs.Parse(args); err != nil {
		return exitGeneric
	}
	if *dir == "" && *manifestPath == "" {
		fmt.Fprintln(os.Stderr, "plan requires --dir or --manifest")
		return exitGeneric
	}
	mpath := *manifestPath
	if mpath == "" {
		mpath = filepath.Join(*dir, "manifest-canonical.json")
	}
	m, err := loadManifest(mpath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitInputCorrupt
	}
	m.Constraints.BudgetMS = *budgetMS
	m.Constraints.MaxBitrateBPS = *maxBitrate
	m.Constraints.MinCoveragePPM = *minCoverage
	m.Constraints.MaxAVSkewMS = *maxSkew

	g := generatedFromDir(*dir)
	job, err := buildNormalJob(g, m)
	if err != nil {
		return printInputError(err)
	}
	pkg, err := manifest.Plan(manifest.PlanRequest{
		Manifest: m, Report: job.index.Report, ClusterCount: len(job.clusters),
		Inputs: job.inputs, InputPrefix: job.prefixSHA,
	})
	if err != nil {
		data, _ := json.MarshalIndent(pkg, "", "  ")
		if len(data) > 0 {
			fmt.Println(string(data))
		}
		fmt.Fprintln(os.Stderr, "plan failed:", err)
		return classifyPlanError(err)
	}
	sum, _ := pkg.SHA256()
	out, _ := pkg.CanonicalJSON()
	fmt.Println(string(out))
	fmt.Printf("config_sha256=%s\n", sum)
	return exitOK
}

func runResume(args []string) int {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	jobDir := fs.String("dir", "", "checkpoint/output job directory")
	fixtureDir := fs.String("fixture-dir", "", "generated fixture directory")
	lfeMix := fs.Bool("lfe-mix", false, "explicitly change LFE policy to create a config conflict")
	if err := fs.Parse(args); err != nil {
		return exitGeneric
	}
	if *jobDir == "" || *fixtureDir == "" {
		fmt.Fprintln(os.Stderr, "resume requires --dir and --fixture-dir")
		return exitGeneric
	}
	m, err := loadManifest(filepath.Join(*fixtureDir, "manifest-canonical.json"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitInputCorrupt
	}
	if *lfeMix {
		m.DefaultLFEMix = true
	}
	g := generatedFromDir(*fixtureDir)
	job, err := buildNormalJob(g, m)
	if err != nil {
		return printInputError(err)
	}
	pkg, err := manifest.Plan(manifest.PlanRequest{
		Manifest: m, Report: job.index.Report, ClusterCount: len(job.clusters),
		Inputs: job.inputs, InputPrefix: job.prefixSHA,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "plan failed:", err)
		return classifyPlanError(err)
	}
	p := resume.Pipeline{
		Dir: *jobDir, JobID: "cli-resume", Manifest: m, Config: pkg,
		Timeline: job.index, Clusters: job.clusters,
	}
	result, err := p.Run()
	if err != nil {
		if media.IsConflict(err) {
			fmt.Println("checkpoint_conflict=true old_output_preserved=true")
			return exitCheckpointConflict
		}
		fmt.Fprintln(os.Stderr, "resume failed:", err)
		return exitGeneric
	}
	fmt.Printf("resume_ok output_sha256=%s processed=%v skipped=%v\n", result.OutputSHA256, result.Processed, result.Skipped)
	return exitOK
}

func loadManifest(path string) (*manifest.Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m manifest.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, media.NewError(media.StageManifest, 0, path, err).WithKind("json")
	}
	return &m, nil
}

func generatedFromDir(dir string) *fixture.Generated {
	if dir == "" {
		return &fixture.Generated{}
	}
	return &fixture.Generated{
		Dir:              dir,
		NormalMKV:        filepath.Join(dir, "normal.mkv"),
		InterleavedMKV:   filepath.Join(dir, "interleaved.mkv"),
		UnknownLengthMKV: filepath.Join(dir, "unknown-length.mkv"),
		RTPWrap:          filepath.Join(dir, "rtp-wrap.bin"),
		EMSGMP4:          filepath.Join(dir, "switch-hints.mp4"),
	}
}

func classifyPlanError(err error) int {
	switch {
	case media.IsConstraintFailure(err):
		return exitConstraint
	case media.IsInputCorruption(err):
		return exitInputCorrupt
	case errors.Is(err, media.ErrColorConflict),
		errors.Is(err, media.ErrFieldConflict),
		errors.Is(err, media.ErrUnknownLayout),
		errors.Is(err, media.ErrUnsupportedVersion):
		return exitInputCorrupt
	default:
		return exitGeneric
	}
}
