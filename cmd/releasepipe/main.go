// releasepipe is the terminal entry point for the local media release
// kernel. Exit codes: 0 = success, 1 = input/processing failure,
// 2 = argument error.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"releasepipe/internal/errs"
	"releasepipe/internal/fixture"
	"releasepipe/internal/index"
	"releasepipe/internal/ingest"
	"releasepipe/internal/release"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) < 1 || args[0] != "demo" {
		fmt.Fprintln(os.Stderr, "usage: releasepipe demo --case good|damaged|interrupted "+
			"[--platform ios,android] --index DIR [--stop-after-block N] [--resume] [--rollback]")
		return 2
	}
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	caseName := fs.String("case", "good", "fixture case: good|damaged|interrupted")
	platformFlag := fs.String("platform", "ios,android", "comma-separated target platforms")
	indexDir := fs.String("index", "", "index/work directory (required)")
	stopAfter := fs.Int("stop-after-block", 0, "stop after N committed blocks (0 = no stop)")
	resume := fs.Bool("resume", false, "resume from the last complete checkpoint block")
	rollback := fs.Bool("rollback", false, "roll back to the most recent complete version")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *indexDir == "" {
		fmt.Fprintln(os.Stderr, "error: --index is required")
		return 2
	}
	if *rollback {
		return doRollback(*indexDir)
	}
	var damaged bool
	switch *caseName {
	case "good", "interrupted":
	case "damaged":
		damaged = true
	default:
		fmt.Fprintf(os.Stderr, "error: unknown case %q\n", *caseName)
		return 2
	}
	platforms := strings.Split(*platformFlag, ",")
	for _, p := range platforms {
		if strings.TrimSpace(p) == "" {
			fmt.Fprintln(os.Stderr, "error: empty platform name")
			return 2
		}
	}
	if err := demo(*indexDir, platforms, damaged, *stopAfter, *resume); err != nil {
		var pe *errs.Error
		if errors.As(err, &pe) {
			fmt.Fprintf(os.Stderr, "error %s\n", pe)
		} else {
			fmt.Fprintf(os.Stderr, "error %v\n", err)
		}
		return 1
	}
	return 0
}

func demo(indexDir string, platforms []string, damaged bool, stopAfter int, resume bool) error {
	fixtureDir := filepath.Join(indexDir, "fixtures")
	if err := fixture.Generate(fixtureDir, damaged); err != nil {
		return errs.Wrap(errs.StageIngest, errs.CodeInternal, fixtureDir, -1, err)
	}
	mainMP4 := filepath.Join(fixtureDir, fixture.MainMP4)
	transMP4 := filepath.Join(fixtureDir, fixture.TransitionMP4)

	// Ingest containers and packet streams into the unified timeline.
	mainTracks, mainMeta, err := ingest.ReadMP4(mainMP4)
	if err != nil {
		return err
	}
	transTracks, transMeta, err := ingest.ReadMP4(transMP4)
	if err != nil {
		return err
	}
	tracks := append(mainTracks, transTracks...)
	video, err := ingest.ReadRTPFile(filepath.Join(fixtureDir, fixture.VideoRTP), "video", 90000)
	if err != nil {
		return err
	}
	audio, err := ingest.ReadRTPFile(filepath.Join(fixtureDir, fixture.AudioRTP), "audio", 48000)
	if err != nil {
		return err
	}
	tl, err := ingest.BuildTimeline(tracks, []*ingest.PacketStream{video, audio})
	if err != nil {
		return err
	}
	syncMS, err := tl.SyncErrorMS()
	if err != nil {
		return err
	}

	// Key everything by index-dir-relative paths so the manifest is
	// byte-identical no matter where the index directory lives.
	rel := func(p string) string {
		if r, err := filepath.Rel(indexDir, p); err == nil {
			return r
		}
		return p
	}
	for _, t := range tracks {
		t.SourcePath = rel(t.SourcePath)
	}

	// Content digests over source bytes; container metadata feeds the
	// per-asset digest so a pure rewrap still invalidates its assets.
	srcDigests := map[string]string{}
	for _, p := range []string{mainMP4, transMP4, video.SourcePath, audio.SourcePath} {
		d, err := index.HashFile(p)
		if err != nil {
			return err
		}
		srcDigests[rel(p)] = d
	}
	containerMeta := map[string]map[string]string{
		rel(mainMP4): mainMeta, rel(transMP4): transMeta,
	}

	store, err := index.OpenStore(indexDir)
	if err != nil {
		return err
	}
	assets := release.BuildAssets(tl, platforms, srcDigests, containerMeta)
	res, err := release.Process(indexDir, assets, platforms, srcDigests, store, stopAfter, resume)
	if err != nil {
		return err
	}
	if res.Interrupted {
		fmt.Printf("interrupted=1\nnext_block=%d\n", res.NextBlock+1) // 1-based for operators
		return errs.New(errs.StageRelease, errs.CodeInterrupted, "", int64(res.NextBlock),
			"stopped after block %d; rerun with --resume", res.NextBlock+1)
	}

	stats := video.Stats
	stats.Received += audio.Stats.Received
	stats.Duplicated += audio.Stats.Duplicated
	stats.Lost += audio.Stats.Lost
	stats.Recovered += audio.Stats.Recovered
	fmt.Printf("sync_ms=%.2f\n", syncMS)
	fmt.Printf("recovered_pct=%.1f\n", stats.RecoveredPct())
	fmt.Printf("p95_ms=%.2f\n", p95MS(res.Durations))
	fmt.Printf("reused_assets=%d\n", res.Reused)
	fmt.Printf("rebuilt_assets=%d\n", res.Rebuilt)
	fmt.Printf("manifest_sha256=%s\n", res.ManifestSHA)
	return nil
}

func doRollback(indexDir string) int {
	v, err := release.Rollback(indexDir)
	if err != nil {
		var pe *errs.Error
		if errors.As(err, &pe) {
			fmt.Fprintf(os.Stderr, "error %s\n", pe)
		} else {
			fmt.Fprintf(os.Stderr, "error %v\n", err)
		}
		return 1
	}
	fmt.Printf("rollback_version=%d\n", v.Number)
	fmt.Printf("manifest_sha256=%s\n", v.ManifestSHA)
	return 0
}

// p95MS returns the 95th percentile of per-asset processing times.
func p95MS(ds []time.Duration) float64 {
	if len(ds) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), ds...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := (95*len(sorted) + 99) / 100 // ceil(0.95*n)
	return float64(sorted[rank-1].Microseconds()) / 1000.0
}
