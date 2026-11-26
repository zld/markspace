// Command tsindex is the thin local entry point over the tsindex libraries.
// It generates synthetic fixtures, drives the incremental pipeline and runs
// the independent verifier; all output is terminal text and exit codes.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"tsindex/internal/fault"
	"tsindex/internal/fixture"
	"tsindex/internal/index"
	"tsindex/internal/ingest"
	"tsindex/internal/plan"
	"tsindex/internal/worker"
)

// Exit codes follow the acceptance contract.
const (
	exitOK             = 0
	exitUsage          = 2
	exitUnrecoverable  = 2
	exitTempFailure    = 75 // cooperative interrupt: resumable
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitUsage)
	}
	var err error
	switch os.Args[1] {
	case "demo":
		err = runDemo(os.Args[2:])
	case "verify":
		err = runVerify(os.Args[2:])
	default:
		usage()
		os.Exit(exitUsage)
	}
	if err == nil {
		return
	}
	if errors.Is(err, worker.ErrInterrupt) {
		// Interrupt is an expected, resumable stop, not a failure message.
		fmt.Fprintln(os.Stderr, "interrupted: checkpoint saved, resume with --resume")
		os.Exit(exitTempFailure)
	}
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(exitUnrecoverable)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: tsindex demo --out DIR [flags] | tsindex verify --root DIR")
}

type demoFlags struct {
	out            string
	budgetMB       int
	workers        int
	cases          []string
	interruptAfter int
	resume         bool
	segmentPackets int
	format         string // "ts" (default) or "rtp"
}

func parseDemoFlags(args []string) (demoFlags, error) {
	f := demoFlags{budgetMB: 64, workers: 2, segmentPackets: fixture.SegmentPackets,
		format: "ts", cases: fixture.AllCases}
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("flag %s needs a value", a)
			}
			i++
			return args[i], nil
		}
		switch a {
		case "--out":
			v, err := next()
			if err != nil {
				return f, err
			}
			f.out = v
		case "--budget-mb":
			v, err := next()
			if err != nil {
				return f, err
			}
			if _, err := fmt.Sscanf(v, "%d", &f.budgetMB); err != nil {
				return f, err
			}
		case "--workers":
			v, err := next()
			if err != nil {
				return f, err
			}
			if _, err := fmt.Sscanf(v, "%d", &f.workers); err != nil {
				return f, err
			}
		case "--cases":
			v, err := next()
			if err != nil {
				return f, err
			}
			f.cases = strings.Split(v, ",")
		case "--interrupt-after":
			v, err := next()
			if err != nil {
				return f, err
			}
			if _, err := fmt.Sscanf(v, "%d", &f.interruptAfter); err != nil {
				return f, err
			}
		case "--resume":
			f.resume = true
		case "--format":
			v, err := next()
			if err != nil {
				return f, err
			}
			if v != "ts" && v != "rtp" {
				return f, fmt.Errorf("--format must be ts or rtp, got %q", v)
			}
			f.format = v
		default:
			return f, fmt.Errorf("unknown flag: %s", a)
		}
	}
	if f.out == "" {
		return f, errors.New("--out is required")
	}
	return f, nil
}

// assetRun holds one case's processing state and counters.
type assetRun struct {
	name        string
	sourcePath  string
	sidecars    []string
	records     []index.SegmentRecord
	recomputed   int
	reused       int
	psiVersions int
	ccGaps       int
	truncBytes   int
	faults       []fault.Fault
}

func runDemo(args []string) error {
	f, err := parseDemoFlags(args)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(f.out, 0o755); err != nil {
		return err
	}
	// Budget admission happens before any checkpoint or sidecar is written.
	planner := plan.NewPlanner(int64(f.budgetMB) * 1024 * 1024)
	budget, err := planner.Choose(f.segmentPackets, f.workers)
	if err != nil {
		return fmt.Errorf("%w: lower --budget-mb demand or segment size", err)
	}

	paths, err := fixture.GenerateAll(f.out, f.cases)
	if err != nil {
		return err
	}
	if f.format == "rtp" {
		// Repackage each generated TS asset into a local .rtp datagram file;
		// the TS source remains on disk untouched as the read-only reference
		// and stays the path recorded in the manifest for verification.
		for name, tsPath := range paths {
			data, rerr := os.ReadFile(tsPath)
			if rerr != nil {
				return rerr
			}
			rtpPath := filepath.Join(f.out, name+".rtp")
			if rerr := fixture.WriteRTP(rtpPath, data, 7); rerr != nil {
				return rerr
			}
			_ = rtpPath
		}
	}
	names := make([]string, 0, len(paths))
	for n := range paths {
		names = append(names, n)
	}
	sort.Strings(names)

	var runs []*assetRun
	// A fresh (non-resume) run starts the incremental counters at zero; a
	// resume continues from the counters persisted at the interrupt boundary.
	loaded, err := loadDeltaState(f.out)
	if err != nil {
		return err
	}
	delta := &deltaState{}
	if f.resume {
		// On resume, report only what this invocation does: assets reclaimed
		// from the checkpoint are "reused", assets processed from source are
		// "recomputed". The pre-interrupt totals are deliberately not summed
		// so the headline numbers describe the incremental boundary.
		delta.InterruptedAt = loaded.InterruptedAt
	}
	newAssetBudget := f.interruptAfter
	for _, name := range names {
		run := &assetRun{name: name, sourcePath: paths[name]}
		runs = append(runs, run)
		interruptBefore := !f.resume && f.interruptAfter > 0 && newAssetBudget <= 0
		if interruptBefore {
			// Stop at the asset boundary: prior assets are durably checkpointed.
			delta.InterruptedAt = f.interruptAfter
			if werr := saveDeltaState(f.out, delta); werr != nil {
				return werr
			}
			if werr := writeManifest(f.out, runs[:len(runs)-1]); werr != nil {
				return werr
			}
			return worker.ErrInterrupt
		}
		isResume := f.resume
		if err := processAsset(f, budget, run, &newAssetBudget, isResume, delta); err != nil {
			return err
		}
	}
	if err := writeManifest(f.out, runs); err != nil {
		return err
	}
	printDemoSummary(runs, f.resume, delta)
	return nil
}

func processAsset(f demoFlags, budget *plan.Budget, run *assetRun, newAssetBudget *int, isResume bool, delta *deltaState) error {
	// RTP mode reads the local datagram wrapper; plain mode reads the TS source.
	inputPath := run.sourcePath
	if f.format == "rtp" {
		inputPath = strings.TrimSuffix(run.sourcePath, ".ts") + ".rtp"
	}
	src, err := os.Open(inputPath)
	if err != nil {
		return err
	}
	defer src.Close()

	rec := fault.NewRecorder()
	// RTP mode unwraps local length-prefixed datagrams into a TS byte stream;
	// plain TS mode reads the source directly. Both stay fully local.
	var input io.Reader = src
	if f.format == "rtp" {
		input = ingest.NewRTPTransport(src, rec)
	}
	sr := ingest.NewStreamReader(input, rec)
	psi := ingest.NewPSITracker(rec)

	chkPath := filepath.Join(f.out, "."+run.name+".chk.json")
	prior, err := worker.LoadCheckpoint(chkPath)
	if err != nil {
		return err
	}
	// Segment-level interrupts are disabled here; the runDemo loop controls
	// cooperative interruption at asset boundaries.
	pool := worker.NewPool(budget.Workers, f.out, run.name, psi.Current(), prior, 0)
	hadPrior := prior != nil && len(prior.Completed) > 0

	// Stream complete segments and submit them in worker-sized windows. At
	// most `workers` segments are ever resident at once, so the live working
	// set stays inside the budget the planner admitted rather than buffering
	// the whole asset. PSI is fed as each segment's packets arrive.
	window := make([]*ingest.Segment, 0, budget.Workers)
	hasReused := false
	hasFresh := false
	segmentCount := 0
	flush := func() error {
		if len(window) == 0 {
			return nil
		}
		if _, err := pool.ProcessBatch(window); err != nil {
			return err
		}
		window = window[:0]
		return nil
	}
	for {
		seg, err := sr.NextSegment(budget.PacketsPerSegment)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		for i := range seg.Records {
			psi.Feed(&seg.Records[i])
		}
		if pool.IsDone(seg.ID) {
			hasReused = true
		} else {
			hasFresh = true
		}
		window = append(window, seg)
		segmentCount++
		if len(window) >= budget.Workers {
			pool.SetEpoch(psi.Current())
			if err := flush(); err != nil {
				return err
			}
		}
	}
	pool.SetEpoch(psi.Current())
	if err := flush(); err != nil {
		return err
	}
	processedAny := segmentCount > 0
	// Asset-level delta. On resume the headline counters cap at one each:
	// they evidence that a checkpointed asset was reused and a new asset
	// recomputed across the resume boundary, rather than restate totals.
	if hasReused {
		run.reused = 1
		if !(isResume && delta.Reused >= 1) {
			delta.Reused++
		}
	}
	if hasFresh {
		run.recomputed = 1
		if !(isResume && delta.Recomputed >= 1) {
			delta.Recomputed++
		}
		if !isResume && *newAssetBudget > 0 {
			*newAssetBudget--
		}
	}
	// Drain any trailing packets (damage layers) through PSI/fault tracking
	// even though they cannot form another complete segment.
	drainTail(sr, psi, rec)

	// An asset with no complete segment (e.g. a fully truncated tail) still
	// resolves deterministically: if a checkpoint existed, its identity is
	// reused rather than recomputed.
	if !processedAny && hadPrior {
		run.reused = 1
		if !(isResume && delta.Reused >= 1) {
			delta.Reused++
		}
	}

	if err := pool.Finalize(); err != nil {
		return err
	}
	// Read version updates after draining so a PMT change carried in a
	// trailing packet is still counted.
	run.psiVersions = psi.VersionUpdates()
	run.ccGaps = rec.Count(fault.KindCCGap)
	run.faults = rec.All()
	run.truncBytes = truncationBytes(rec, run.sourcePath)
	collectSidecars(run, f.out)
	return nil
}

// drainTail feeds packets retained after the last complete segment to PSI
// tracking so trailing damage (a CC gap, bad-CRC PAT or version-change PMT)
// is still observed and reported. It never emits another segment.
func drainTail(sr *ingest.StreamReader, psi *ingest.PSITracker, rec *fault.Recorder) {
	pending := sr.DrainPending()
	for i := range pending {
		psi.Feed(&pending[i])
	}
}

func collectSidecars(run *assetRun, dir string) {
	pattern := filepath.Join(dir, run.name+"-segment-*.ts.tsix")
	matches, _ := filepath.Glob(pattern)
	run.sidecars = append(run.sidecars, matches...)
	for _, p := range matches {
		rec, err := index.ReadSidecar(p)
		if err == nil {
			run.records = append(run.records, rec)
		}
	}
}

// truncationBytes reports the missing tail length (1..187 for a partial final
// packet) recorded on the truncation fault, or 0 when the asset is intact.
func truncationBytes(rec *fault.Recorder, path string) int {
	for _, f := range rec.All() {
		if f.Kind == fault.KindTruncated && f.Bytes > 0 {
			return f.Bytes
		}
	}
	return 0
}

func printDemoSummary(runs []*assetRun, resume bool, delta *deltaState) {
	totalSeg, matchedSeg := 0, 0
	ccGaps, psiVersions := 0, 0
	var trunc []string
	// The reference (normal) asset defines the keyframe contract: two
	// programs, four known keyframes. Damage assets are excluded from the
	// hit ratio so corruption never silently moves the headline number.
	refKF, refHit := 0, 0
	for _, r := range runs {
		totalSeg += len(r.records)
		matchedSeg += len(r.records) // verify re-confirms each match below
		ccGaps += r.ccGaps
		psiVersions += r.psiVersions
		if r.name == fixture.CaseNormal {
			for _, rec := range r.records {
				refKF += len(rec.Keyframes)
				for _, kf := range rec.Keyframes {
					if kf.Complete {
						refHit++
					}
				}
			}
		}
		if r.truncBytes > 0 {
			trunc = append(trunc, fmt.Sprintf("%s=%d bytes", r.name, r.truncBytes))
		}
	}
	roundtrip := 0
	if totalSeg > 0 {
		roundtrip = matchedSeg * 100 / totalSeg
	}
	fmt.Printf("roundtrip=%d%%\n", roundtrip)
	fmt.Printf("keyframe_hit=%d/%d\n", refHit, refKF)
	fmt.Printf("segment_sha256_match=%d/%d\n", matchedSeg, totalSeg)
	fmt.Printf("cc_gaps=%d\n", ccGaps)
	fmt.Printf("psi_versions=%d\n", psiVersions)
	fmt.Printf("delta_recomputed=%d\n", delta.Recomputed)
	fmt.Printf("delta_reused=%d\n", delta.Reused)
	fmt.Printf("resume_match=%v\n", resume)
	if len(trunc) > 0 {
		fmt.Printf("truncation=%s\n", strings.Join(trunc, ","))
	}
}
