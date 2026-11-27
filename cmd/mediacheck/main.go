// Command mediacheck is the terminal entry point: it generates local
// samples, runs the media processing pipeline and prints acceptance data.
// Subcommands: demo, verify, resume.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"time"

	"mediacheck/internal/fixture"
	"mediacheck/internal/index"
	"mediacheck/internal/provenance"
	"mediacheck/internal/timeline"
	"mediacheck/internal/txn"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	var code int
	switch os.Args[1] {
	case "demo":
		code = cmdDemo(os.Args[2:])
	case "verify":
		code = cmdVerify(os.Args[2:])
	case "resume":
		code = cmdResume(os.Args[2:])
	default:
		usage()
		code = 1
	}
	os.Exit(code)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: mediacheck <demo|verify|resume> [flags]")
	fmt.Fprintln(os.Stderr, "  demo   --format both|hls|dash --case normal|truncated|backward|noduration --out DIR")
	fmt.Fprintln(os.Stderr, "  verify --dir DIR --from-ns N --to-ns N")
	fmt.Fprintln(os.Stderr, "  resume --dir DIR")
}

type report struct {
	command        string
	events         int
	startNs, endNs int64
	entries        int
	fromNs, toNs   int64
	status         index.RangeStatus
	hits           int
	firstB, lastB  int64
	anomalies      []index.Anomaly
	affected       int
	budgetMs       float64
	actualMs       float64
	recovery       string
	rolledBack     int
	resumedStage   string
	identityState  string
	invalidation   string
	inputDigest    string
	finalDigest    string
	cleanDigest    string
	digestMatch    bool
	showDigest     bool
	conclusion     string
	exitCode       int
}

func (r *report) print() {
	fmt.Println("== mediacheck report ==")
	fmt.Printf("command=%s\n", r.command)
	fmt.Printf("events=%d\n", r.events)
	fmt.Printf("time_range_ns=[%d,%d)\n", r.startNs, r.endNs)
	fmt.Printf("index_entries=%d\n", r.entries)
	fmt.Printf("range_read=[%d,%d) status=%s hits=%d first_boundary_ns=%d last_boundary_ns=%d\n",
		r.fromNs, r.toNs, r.status, r.hits, r.firstB, r.lastB)
	fmt.Printf("anomaly_ranges=%d\n", len(r.anomalies))
	for i, a := range r.anomalies {
		fmt.Printf("anomaly[%d]=[%d,%d) kind=%s source=%s detail=%q\n",
			i, a.StartNs, a.EndNs, a.Kind, a.Source, a.Detail)
	}
	fmt.Printf("affected_entries=%d\n", r.affected)
	within := r.actualMs <= r.budgetMs
	fmt.Printf("budget=100ms/100events budget_ms=%.0f actual_ms=%.3f within_budget=%t\n",
		r.budgetMs, r.actualMs, within)
	if !within {
		fmt.Printf("budget_deviation_ms=%.3f\n", r.actualMs-r.budgetMs)
	}
	fmt.Printf("txn_recovery=%s rolled_back=%d resumed_stage=%s\n",
		r.recovery, r.rolledBack, r.resumedStage)
	fmt.Printf("identity=%s invalidation=%s\n", r.identityState, r.invalidation)
	fmt.Printf("input_digest=%s\n", r.inputDigest)
	if r.showDigest {
		fmt.Printf("final_digest=%s clean_digest=%s digest_match=%t\n",
			r.finalDigest, r.cleanDigest, r.digestMatch)
	}
	fmt.Printf("conclusion=%s\n", r.conclusion)
	// Printed so the intended process exit code is observable even when
	// `go run` collapses a non-zero child status to its own exit 1.
	fmt.Printf("exit_code=%d\n", r.exitCode)
}

func newReport(command string, res *provenance.Result, fromNs, toNs int64, anomalies []index.Anomaly, elapsed time.Duration) *report {
	rr := res.Index.Read(fromNs, toNs)
	r := &report{
		command:      command,
		events:       res.Events,
		startNs:      res.Index.MediaStartNs,
		endNs:        res.Index.MediaEndNs,
		entries:      len(res.Index.Entries),
		fromNs:       fromNs,
		toNs:         toNs,
		status:       rr.Status,
		hits:         len(rr.Hits),
		firstB:       rr.FirstBoundaryNs,
		lastB:        rr.LastBoundaryNs,
		anomalies:    anomalies,
		affected:     provenance.AffectedEntries(res, anomalies),
		budgetMs:     math.Ceil(float64(res.Events)/100.0) * 100.0,
		actualMs:     float64(elapsed.Nanoseconds()) / 1e6,
		recovery:     "none",
		resumedStage: "none",
		inputDigest:  res.Identity.Digest(),
	}
	r.conclusion = "PASS"
	if len(anomalies) > 0 {
		r.conclusion = "REJECT"
	}
	return r
}

func cmdDemo(args []string) int {
	flags := flag.NewFlagSet("demo", flag.ContinueOnError)
	format := flags.String("format", "both", "hls|dash|both")
	caze := flags.String("case", "normal", "normal|truncated|backward|noduration")
	out := flags.String("out", "", "output directory")
	if err := flags.Parse(args); err != nil {
		return 1
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "demo: --out is required")
		return 1
	}
	if _, err := os.Stat(filepath.Join(*out, "fixtures")); errors.Is(err, fs.ErrNotExist) {
		if err := fixture.Generate(*out, fixture.Case(*caze)); err != nil {
			fmt.Fprintln(os.Stderr, "demo: fixture:", err)
			return 1
		}
	}
	start := time.Now()
	oc, err := txn.RunPipeline(*out, *format, "")
	if err != nil {
		return fail(err)
	}
	elapsed := time.Since(start)
	r := newReport("demo", oc.Result, 0, 2_000_000_000, oc.Result.Index.Anomalies, elapsed)
	if oc.Recovery.Interrupted {
		r.recovery = "resumed"
		r.rolledBack = oc.Recovery.RolledBack
		r.resumedStage = oc.Recovery.ResumeStage
	}
	r.identityState = oc.IdentityState
	r.invalidation = oc.Invalidation
	r.print()
	return 0
}

func cmdVerify(args []string) int {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	dir := flags.String("dir", "", "output directory of a previous run")
	from := flags.Int64("from-ns", 0, "range start, nanoseconds (inclusive)")
	to := flags.Int64("to-ns", 0, "range end, nanoseconds (exclusive)")
	if err := flags.Parse(args); err != nil {
		return 1
	}
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "verify: --dir is required")
		return 1
	}
	start := time.Now()
	res, err := provenance.LoadResult(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "verify:", err)
		return 1
	}
	findings, err := provenance.Verify(*dir, res)
	if err != nil {
		fmt.Fprintln(os.Stderr, "verify:", err)
		return 1
	}
	elapsed := time.Since(start)
	r := newReport("verify", res, *from, *to, findings, elapsed)
	r.identityState = "committed"
	r.invalidation = "none"
	if len(findings) > 0 {
		r.exitCode = 2
	}
	r.print()
	return r.exitCode
}

func cmdResume(args []string) int {
	flags := flag.NewFlagSet("resume", flag.ContinueOnError)
	dir := flags.String("dir", "", "output directory to recover and finish")
	if err := flags.Parse(args); err != nil {
		return 1
	}
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "resume: --dir is required")
		return 1
	}
	if _, err := os.Stat(filepath.Join(*dir, "fixtures")); errors.Is(err, fs.ErrNotExist) {
		if err := fixture.Generate(*dir, fixture.CaseNormal); err != nil {
			fmt.Fprintln(os.Stderr, "resume: fixture:", err)
			return 1
		}
	}
	// simulate an interrupted run if this directory has no journal yet
	if w, err := txn.OpenWAL(*dir); err != nil {
		fmt.Fprintln(os.Stderr, "resume:", err)
		return 1
	} else if len(w.Records) == 0 {
		if _, err := txn.RunPipeline(*dir, "both", txn.StageBuild); err != nil && !errors.Is(err, txn.ErrSimulatedCrash) {
			return fail(err)
		}
	}
	start := time.Now()
	oc, err := txn.RunPipeline(*dir, "both", "")
	if err != nil {
		return fail(err)
	}
	elapsed := time.Since(start)
	clean, err := txn.CleanDigest(*dir, "both")
	if err != nil {
		return fail(err)
	}
	r := newReport("resume", oc.Result, 0, 2_000_000_000, oc.Result.Index.Anomalies, elapsed)
	if oc.Recovery.Interrupted {
		r.recovery = "resumed"
		r.rolledBack = oc.Recovery.RolledBack
		r.resumedStage = oc.Recovery.ResumeStage
	}
	r.identityState = oc.IdentityState
	r.invalidation = oc.Invalidation
	r.showDigest = true
	r.finalDigest = provenance.DigestIndex(oc.Result.Index)
	r.cleanDigest = clean
	r.digestMatch = r.finalDigest == r.cleanDigest
	if !r.digestMatch {
		r.conclusion = "REJECT"
	}
	r.print()
	return 0
}

func fail(err error) int {
	if te, ok := timeline.As(err); ok {
		fmt.Printf("error=%s\n", te)
	} else {
		fmt.Printf("error=%v\n", err)
	}
	fmt.Println("conclusion=ERROR")
	return 1
}
