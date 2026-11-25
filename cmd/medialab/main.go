// Command medialab is the terminal-only entry point. It drives the local
// processing library; there is no GUI and no network dependency.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/local/medialab/internal/container"
	"github.com/local/medialab/internal/fixture"
	"github.com/local/medialab/internal/model"
	"github.com/local/medialab/internal/pipeline"
	"github.com/local/medialab/internal/report"
	"github.com/local/medialab/internal/stream"
	"github.com/local/medialab/internal/txn"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "demo":
		if err := runDemo(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "medialab: %v\n", err)
			os.Exit(1)
		}
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "medialab: unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: medialab demo [flags]")
	fmt.Fprintln(os.Stderr, "  --seed int")
	fmt.Fprintln(os.Stderr, "  --loss-burst int")
	fmt.Fprintln(os.Stderr, "  --reorder int")
	fmt.Fprintln(os.Stderr, "  --duplicate int")
	fmt.Fprintln(os.Stderr, "  --bandwidth comma-separated kbps, e.g. 800,300,900")
	fmt.Fprintln(os.Stderr, "  --interrupt-after commit-N (optional)")
}

type demoFlags struct {
	seed        int64
	lossBurst   int
	reorder     int
	duplicate   int
	bandwidth   []int
	interruptAt string
}

func parseDemoFlags(args []string) (demoFlags, error) {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	var (
		seed      = fs.Int("seed", 7, "deterministic sample seed")
		lossBurst = fs.Int("loss-burst", 4, "consecutive media packets dropped")
		reorder   = fs.Int("reorder", 3, "bounded reorder displacement")
		duplicate = fs.Int("duplicate", 2, "duplicated media packets")
		bwText    = fs.String("bandwidth", "800,300,900", "kbps schedule")
		interrupt = fs.String("interrupt-after", "", "commit boundary to crash before")
	)
	if err := fs.Parse(args); err != nil {
		return demoFlags{}, err
	}
	bw, err := parseBandwidth(*bwText)
	if err != nil {
		return demoFlags{}, err
	}
	return demoFlags{
		seed: int64(*seed), lossBurst: *lossBurst, reorder: *reorder,
		duplicate: *duplicate, bandwidth: bw, interruptAt: *interrupt,
	}, nil
}

func parseBandwidth(text string) ([]int, error) {
	parts := strings.Split(text, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		v, err := strconv.Atoi(p)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("invalid bandwidth value %q", p)
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, errors.New("bandwidth schedule must not be empty")
	}
	return out, nil
}

// runDemo executes the three required sample scenarios and prints their
// summaries. Exit code stays zero only when every assertion holds; a
// failed validation surfaces as a non-zero exit from main.
func runDemo(args []string) error {
	f, err := parseDemoFlags(args)
	if err != nil {
		return err
	}
	root := filepath.Join(".medialab-demo", fmt.Sprintf("seed-%d", f.seed))

	// Scenario 1: clean, healthy run.
	cleanDir := filepath.Join(root, "clean")
	cleanCfg := pipeline.Config{
		Seed: f.seed, LossBurst: f.lossBurst, LossAt: 0,
		Reorder: f.reorder, Duplicate: f.duplicate,
		Bandwidth: f.bandwidth, WorkDir: cleanDir,
	}
	clean, err := pipeline.Run(cleanCfg)
	if err != nil {
		return fmt.Errorf("clean sample: %w", err)
	}
	fmt.Println("# clean sample")
	fmt.Println(report.Text(clean.Summary))

	// Scenario 2: damaged samples must report located damage ranges and
	// must not claim a fake success.
	for _, dc := range []struct {
		label string
		kind  fixture.Corruption
	}{
		{"truncated", fixture.CorruptTruncation},
		{"invalid timestamp", fixture.CorruptInvalidTimestamp},
		{"missing index", fixture.CorruptMissingIndex},
	} {
		damagedSummary, err := damagedScenario(f.seed, dc.kind)
		if err != nil {
			return fmt.Errorf("damaged sample (%s): %w", dc.label, err)
		}
		fmt.Printf("# damaged sample (%s)\n", dc.label)
		fmt.Println(report.Text(damagedSummary))
		if len(damagedSummary.DamageRanges) == 0 {
			return fmt.Errorf("damaged sample (%s) reported no damage ranges", dc.label)
		}
	}

	// Scenario 3: interruption then restart. The interruption is injected
	// only while the target boundary is not yet durable; a repeated demo
	// run against the same directory resumes or skips straight through.
	restartDir := filepath.Join(root, "restart")
	restartCfg := cleanCfg
	restartCfg.WorkDir = restartDir
	if f.interruptAt != "" && !boundaryDurable(restartDir, f.interruptAt) {
		crashed := restartCfg
		crashed.InterruptAt = f.interruptAt
		if _, err := pipeline.Run(crashed); !errors.Is(err, txn.ErrInterrupted) {
			return fmt.Errorf("expected interruption, got %v", err)
		}
		fmt.Printf("# interrupted before %s; resuming\n", f.interruptAt)
	}
	restartCfg.InterruptAt = ""
	resumed, err := pipeline.Run(restartCfg)
	if err != nil {
		return fmt.Errorf("resume: %w", err)
	}
	fmt.Println("# restarted sample")
	fmt.Println(report.Text(resumed.Summary))
	fmt.Printf("summary_hash=%s\n", resumed.ContentHash)

	// Restart verification: a second resume must be identical.
	again, err := pipeline.Run(restartCfg)
	if err != nil {
		return fmt.Errorf("second resume: %w", err)
	}
	if again.ContentHash != resumed.ContentHash ||
		again.Summary.CommitID != resumed.Summary.CommitID {
		return errors.New("restart verification failed: commit id or summary hash drifted")
	}

	// Final assertion gate: the clean run must meet every SLA. The damage
	// range check for each damaged variant happens inside the loop above.
	if err := assertClean(clean.Summary); err != nil {
		return err
	}
	return nil
}

// boundaryDurable reports whether the named stage boundary already exists
// in the restart work directory's journal.
func boundaryDurable(workDir, stage string) bool {
	j, err := txn.OpenJournal(filepath.Join(workDir, "txn"))
	if err != nil {
		return false
	}
	defer j.Close()
	return j.Committed(stage)
}

// damagedScenario parses a corrupted MP4 and renders its summary without
// going through the healthy transactional pipeline.
func damagedScenario(seed int64, kind fixture.Corruption) (report.Summary, error) {
	clean, _ := fixture.GenerateMP4(fixture.DefaultMP4Spec(seed))
	damaged := fixture.CorruptMP4(clean, kind)
	// Prefer parsing the damaged bytes so mutated tables (zero stts delta,
	// removed stss) are seen directly. If strict parsing fails, fall back
	// to the clean metadata for structural location of the byte damage.
	meta, err := container.ParseMP4(damaged)
	if err != nil {
		if meta, err = container.ParseMP4(clean); err != nil {
			return report.Summary{}, err
		}
	}
	book := &model.ErrorBook{}
	shortSize := int64(len(damaged))
	stats := stream.AnalyzeFrames(meta.Samples, shortSize, book)
	dmg := stream.DetectDamage(meta, shortSize, stats, book)
	av := stream.AVSync(meta)
	s := report.NewBuilder().
		SetCommitID("damaged-only").
		SetAVSync(av).
		SetKeyframeGap(stats.KeyframeGapMs).
		SetDamage(dmg.Ranges).
		SetErrorCount(len(book.All())).
		MarkHealthy(false).
		Build()
	return s, nil
}

func assertClean(s report.Summary) error {
	var problems []string
	if s.AVSyncMs > 20 {
		problems = append(problems, fmt.Sprintf("av_sync_ms %d > 20", s.AVSyncMs))
	}
	if s.RecoveryRate < 0.75 {
		problems = append(problems, fmt.Sprintf("recovery_rate %.4f < 0.75", s.RecoveryRate))
	}
	if s.P95LatencyMs > 500 {
		problems = append(problems, fmt.Sprintf("p95_latency_ms %d > 500", s.P95LatencyMs))
	}
	if !s.Healthy {
		problems = append(problems, "healthy flag is false")
	}
	if len(problems) > 0 {
		return fmt.Errorf("assertion failure: %s", strings.Join(problems, "; "))
	}
	return nil
}
