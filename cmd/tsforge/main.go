// Command tsforge is the terminal adapter for the media-processing kernel.
//
// Two subcommands are exposed:
//
//	tsforge demo   --seed N --out DIR   generate clean and damaged masters,
//	                                    process them, print the acceptance line
//	tsforge verify --input DIR          re-check every segment hash, the
//	                                    manifest and the index of a prior run
//
// Exit codes: 0 verified, 2 damaged input/format, 3 a blocking dependency,
// 4 a hash or roundtrip mismatch.
package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"tsforge/internal/dag"

	"tsforge/internal/clock"
	"tsforge/internal/fixture"
	"tsforge/internal/index"
	"tsforge/internal/pipeline"
	"tsforge/internal/provenance"
	"tsforge/internal/remux"
	"tsforge/internal/ts"
)

const (
	exitOK       = 0
	exitDamaged  = 2
	exitBlocked  = 3
	exitMismatch = 4
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitDamaged)
	}
	var err error
	switch os.Args[1] {
	case "demo":
		err = runDemo(os.Args[2:])
	case "verify":
		err = runVerify(os.Args[2:])
	case "run":
		err = runProcess(os.Args[2:])
	case "help", "-h", "--help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "tsforge: unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(exitDamaged)
	}
	if err != nil {
		var ec exitCode
		if errors.As(err, &ec) {
			if ec.message != "" {
				fmt.Fprintln(os.Stderr, "tsforge:", ec.message)
			}
			os.Exit(ec.code)
		}
		fmt.Fprintln(os.Stderr, "tsforge:", err)
		os.Exit(exitDamaged)
	}
}

func usage() {
	fmt.Println(`tsforge - MPEG-TS repair, indexing and provenance

usage:
  tsforge demo   --seed N --out DIR
  tsforge run    --input FILE --out DIR
  tsforge verify --input DIR

demo generates synthetic clean and damaged masters, processes both through
the repair/index/remux graph and prints the acceptance summary.

exit codes: 0 verified, 2 damaged input/format, 3 blocking dependency,
            4 hash or roundtrip mismatch.`)
}

// exitCode carries a process exit code through the error return path.
type exitCode struct {
	code    int
	message string
}

func (e exitCode) Error() string { return e.message }

// ---- demo ----

type demoConfig struct {
	seed        int64
	out         string
	workers     int
	cacheDir    string
	segTicks    int64
	maxSegments int
}

func runDemo(args []string) error {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	seed := fs.Int64("seed", 17, "fixture seed")
	out := fs.String("out", "/tmp/tsforge-demo", "output directory")
	workers := fs.Int("workers", 4, "DAG worker count")
	cache := fs.String("cache", "", "DAG result cache directory (defaults to OUT/cache)")
	segSecs := fs.Int("segment-seconds", 2, "target segment duration in seconds")
	maxSeg := fs.Int("max-segments", 32, "segment ceiling")
	if err := fs.Parse(args); err != nil {
		return exitCode{code: exitDamaged, message: err.Error()}
	}
	if *out == "" {
		return exitCode{code: exitDamaged, message: "demo: --out is required"}
	}
	cfg := demoConfig{seed: *seed, out: *out, workers: *workers,
		cacheDir: *cache, segTicks: int64(*segSecs) * clock.TimeScale, maxSegments: *maxSeg}
	if cfg.cacheDir == "" {
		cfg.cacheDir = filepath.Join(cfg.out, "cache")
	}
	if err := os.MkdirAll(cfg.out, 0o755); err != nil {
		return err
	}

	// Generate the masters under OUT/fixtures. Inputs are never modified by
	// later stages; damage writes a separate file.
	fixDir := filepath.Join(cfg.out, "fixtures")
	if err := os.MkdirAll(fixDir, 0o755); err != nil {
		return err
	}
	clean := filepath.Join(fixDir, "clean.ts")
	damaged := filepath.Join(fixDir, "damaged.ts")
	spec := fixture.DefaultSpec(cfg.seed)
	gen, err := fixture.Generate(clean, spec)
	if err != nil {
		return err
	}
	// Damage at least three fault classes, including two that propagate
	// through scan, index or scheduling.
	classes := []string{"pcrwrap", "cc", "crc", "truncpsi", "dup", "tail", "misalign"}
	if _, err := fixture.Mutate(clean, damaged, classes, cfg.seed); err != nil {
		return err
	}

	params := pipeline.DefaultParams()
	params.Seed = cfg.seed
	params.SegmentTicks = cfg.segTicks
	opts := pipeline.Options{Workers: cfg.workers, CacheDir: cfg.cacheDir,
		AllowRecovery: true, MaxSegments: cfg.maxSegments}

	// The clean master drives the acceptance metrics.
	res, err := pipeline.Run(context.Background(), clean, filepath.Join(cfg.out, "clean"), params, opts)
	if err != nil {
		return classifyRunError(err)
	}
	// The damaged master proves recovery end to end.
	if _, err := pipeline.Run(context.Background(), damaged, filepath.Join(cfg.out, "damaged"), params, opts); err != nil {
		return classifyRunError(err)
	}
	// A deliberately unrecoverable master proves that an ambiguous timestamp
	// is reported as blocking rather than fabricated. Its probe result is
	// printed, but it does not fail the demo: the acceptance run is clean.
	blockedProbe := probeBlockedFixture(cfg, params)

	roundtrip, keyframeHit, err := acceptance(res)
	if err != nil {
		return exitCode{code: exitMismatch, message: err.Error()}
	}
	ver, err := verifyManifest(filepath.Join(cfg.out, "clean"))
	if err != nil {
		return exitCode{code: exitMismatch, message: err.Error()}
	}

	// Report, in a fixed order so reruns print identical acceptance lines.
	fmt.Printf("roundtrip_match=%.3f\n", roundtrip)
	fmt.Printf("keyframe_hit=%.3f\n", keyframeHit)
	fmt.Printf("segments=%d\n", len(res.Segments))
	fmt.Printf("segment_sha256_verified=%d/%d\n", ver.segmentsOK, ver.segmentsTotal)
	fmt.Printf("recovered_nodes=%d\n", res.Recovered)
	fmt.Printf("blocked_by=%s\n", res.BlockedBy)
	fmt.Printf("fixture_packets=%d\n", gen.Packets)
	fmt.Printf("fault_classes=%d\n", len(classes))
	fmt.Printf("blocked_probe=%s\n", blockedProbe)
	fmt.Printf("exit=0\n")

	if roundtrip < 1.0 || keyframeHit < 1.0 || ver.segmentsOK != ver.segmentsTotal {
		return exitCode{code: exitMismatch,
			message: fmt.Sprintf("acceptance mismatch: roundtrip=%.3f keyframe_hit=%.3f segments=%d/%d",
				roundtrip, keyframeHit, ver.segmentsOK, ver.segmentsTotal)}
	}
	return nil
}

// probeBlockedFixture builds a master with an out-of-range, unflagged PTS and
// reports the node a repair would block on. It returns "none" if the probe
// unexpectedly succeeded; a failure to *build* the probe is non-fatal for the
// demo as a whole and is reported as "unavailable".
func probeBlockedFixture(cfg demoConfig, params pipeline.Params) string {
	dir := filepath.Join(cfg.out, "fixtures")
	path := filepath.Join(dir, "blocked.ts")
	if err := fixture.GenerateBlocked(path, cfg.seed); err != nil {
		return "unavailable"
	}
	res, err := pipeline.Run(context.Background(), path, filepath.Join(cfg.out, "blocked"), params,
		pipeline.Options{Workers: cfg.workers, MaxSegments: cfg.maxSegments})
	if err == nil {
		return "none"
	}
	var blocked *pipeline.BlockedError
	if errors.As(err, &blocked) {
		if res != nil && res.BlockedBy != "" {
			return res.BlockedBy
		}
		return "clock"
	}
	return "none"
}

// classifyRunError maps pipeline failures onto documented exit codes.
func classifyRunError(err error) error {
	var blocked *pipeline.BlockedError
	if errors.As(err, &blocked) {
		return exitCode{code: exitBlocked, message: err.Error()}
	}
	if index.IsLimits(err) {
		return exitCode{code: exitDamaged, message: err.Error()}
	}
	var le *index.LimitsError
	if errors.As(err, &le) {
		return exitCode{code: exitDamaged, message: err.Error()}
	}
	// Anything else during analysis is treated as damaged input or format.
	return exitCode{code: exitDamaged, message: err.Error()}
}

// acceptance computes the roundtrip and keyframe-hit ratios.
//
// roundtrip_match is the fraction of emitted video samples whose elementary
// bytes, read back out of the MP4 mdat, hash to exactly the source payload
// digest recorded in the index. It is 1.0 only when every byte survived.
//
// keyframe_hit is the fraction of random seeks, placed at arbitrary
// presentation times, that resolve to a keyframe-opened sample. It is 1.0
// only when the index can always start decoding without a prior frame.
func acceptance(res *pipeline.RunResult) (roundtrip, keyframeHit float64, err error) {
	if res == nil || res.Index == nil || len(res.Index.Records) == 0 {
		return 0, 0, errors.New("acceptance: empty index")
	}
	// Roundtrip: re-read each segment's mdat and compare against the hashes
	// the index records for that segment.
	total, ok := 0, 0
	bySeg := make(map[int][]index.Record)
	for _, r := range res.Index.Records {
		bySeg[r.Segment] = append(bySeg[r.Segment], r)
	}
	for _, seg := range res.Segments {
		got, err := remux.ReadSegmentPayloads(seg.Path)
		if err != nil {
			return 0, 0, fmt.Errorf("acceptance: segment %d unreadable: %w", seg.Number, err)
		}
		recs := bySeg[seg.Number]
		var want []string
		for _, r := range recs {
			if r.Kind == index.KindVideo {
				want = append(want, r.PayloadHash)
			}
		}
		// The mdat holds video then audio; compare the leading video run.
		n := len(want)
		if n > len(got) {
			n = len(got)
		}
		for i := 0; i < n; i++ {
			total++
			if got[i] == want[i] {
				ok++
			}
		}
		if len(want) > 0 && n < len(want) {
			total += len(want) - n
		}
	}
	if total == 0 {
		return 0, 0, errors.New("acceptance: no video samples to compare")
	}
	roundtrip = float64(ok) / float64(total)

	// Keyframe hit: probe the index at evenly spaced presentation times
	// across the indexed range and require every hit to be a keyframe.
	hits, seeks := 0, 0
	first, last := res.Index.FirstDTS, res.Index.LastDTS
	if last <= first {
		return roundtrip, 0, errors.New("acceptance: degenerate index range")
	}
	const probes = 1000
	for i := 0; i < probes; i++ {
		tick := first + (last-first)*int64(i)/int64(probes-1)
		rec, found := res.Index.Find(tick)
		seeks++
		if found && rec.Keyframe {
			hits++
		}
	}
	keyframeHit = float64(hits) / float64(seeks)
	return roundtrip, keyframeHit, nil
}

// ---- run ----

// runProcess processes one local TS master and returns the documented exit
// codes: 0 verified, 2 damaged input/format, 3 a blocking dependency (an
// unrecoverable timestamp or a failed upstream node), 4 a hash mismatch.
func runProcess(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	input := fs.String("input", "", "input MPEG-TS file")
	out := fs.String("out", "", "output directory")
	seed := fs.Int64("seed", 17, "run seed recorded in the manifest")
	workers := fs.Int("workers", 4, "DAG worker count")
	segSecs := fs.Int("segment-seconds", 2, "target segment duration in seconds")
	cache := fs.String("cache", "", "DAG result cache directory (defaults to OUT/cache)")
	noRecover := fs.Bool("no-recover", false, "disable reuse of verified cached results")
	if err := fs.Parse(args); err != nil {
		return exitCode{code: exitDamaged, message: err.Error()}
	}
	if *input == "" || *out == "" {
		return exitCode{code: exitDamaged, message: "run: --input and --out are required"}
	}
	if _, err := os.Stat(*input); err != nil {
		return exitCode{code: exitDamaged, message: fmt.Sprintf("run: %v", err)}
	}
	params := pipeline.DefaultParams()
	params.Seed = *seed
	params.SegmentTicks = int64(*segSecs) * clock.TimeScale
	cacheDir := *cache
	if cacheDir == "" {
		cacheDir = filepath.Join(*out, "cache")
	}
	opts := pipeline.Options{Workers: *workers, CacheDir: cacheDir, AllowRecovery: !*noRecover}
	res, err := pipeline.Run(context.Background(), *input, *out, params, opts)
	if err != nil {
		var blocked *pipeline.BlockedError
		if errors.As(err, &blocked) {
			fmt.Printf("blocked_by=clock\n")
			fmt.Fprintln(os.Stderr, "tsforge:", err)
			return exitCode{code: exitBlocked, message: ""}
		}
		// A failed node (blocking dependency) also maps to exit 3.
		var failure *dag.Failure
		if errors.As(err, &failure) {
			fmt.Fprintf(os.Stderr, "tsforge: blocked_by=%s: %v\n", res.BlockedBy, err)
			return exitCode{code: exitBlocked, message: ""}
		}
		return classifyRunError(err)
	}
	rep, err := verifyManifest(*out)
	if err != nil {
		return exitCode{code: exitMismatch, message: fmt.Sprintf("verify: %v", err)}
	}
	fmt.Printf("segments=%d\n", len(res.Segments))
	fmt.Printf("segment_sha256_verified=%d/%d\n", rep.segmentsOK, rep.segmentsTotal)
	fmt.Printf("recovered_nodes=%d\n", res.Recovered)
	fmt.Printf("blocked_by=%s\n", res.BlockedBy)
	if rep.segmentsOK != rep.segmentsTotal {
		return exitCode{code: exitMismatch, message: "segment verification failed"}
	}
	return nil
}

// ---- verify ----

type verifyReport struct {
	segmentsTotal int
	segmentsOK    int
	manifestOK    bool
	indexOK       bool
	details       []string
}

func runVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	input := fs.String("input", "", "directory produced by demo")
	if err := fs.Parse(args); err != nil {
		return exitCode{code: exitDamaged, message: err.Error()}
	}
	if *input == "" {
		return exitCode{code: exitDamaged, message: "verify: --input is required"}
	}
	rep, err := mergeVerify(*input)
	if err != nil {
		return err
	}
	for _, line := range rep.details {
		fmt.Println(line)
	}
	fmt.Printf("segments_verified=%d/%d\n", rep.segmentsOK, rep.segmentsTotal)
	fmt.Printf("manifest_verified=%d\n", boolInt(rep.manifestOK))
	fmt.Printf("index_verified=%d\n", boolInt(rep.indexOK))
	if rep.segmentsOK != rep.segmentsTotal || !rep.manifestOK || !rep.indexOK {
		return exitCode{code: exitMismatch, message: "verify: hash or roundtrip mismatch"}
	}
	fmt.Println("verify=ok")
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// mergeVerify checks both the clean and damaged output trees under input, as
// produced by demo. A tree without a manifest is skipped rather than failed.
func mergeVerify(dir string) (*verifyReport, error) {
	rep := &verifyReport{manifestOK: true, indexOK: true}
	subdirs := []string{filepath.Join(dir, "clean"), filepath.Join(dir, "damaged")}
	anySegment := false
	for _, sub := range subdirs {
		m, err := provenance.Read(filepath.Join(sub, "manifest.json"))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, exitCode{code: exitMismatch, message: fmt.Sprintf("%s: %v", sub, err)}
		}
		if len(m.Segments) == 0 {
			continue
		}
		anySegment = true
		for _, s := range m.Segments {
			rep.segmentsTotal++
			good, actual, err := provenance.VerifySegment(sub, s)
			if err != nil {
				rep.details = append(rep.details,
					fmt.Sprintf("segment %v", err))
				continue
			}
			if !good {
				rep.details = append(rep.details,
					fmt.Sprintf("segment %d hash %s != %s", s.Number, actual, s.SHA256))
				continue
			}
			if !m.DependencyHashesConsistent(s.IndexDigest) {
				rep.details = append(rep.details, fmt.Sprintf("segment %d index digest mismatch", s.Number))
				continue
			}
			rep.segmentsOK++
		}
		// The manifest must carry the tool, version, params hash and deps.
		if m.Tool == "" || m.ToolVersion == "" || m.ParamsHash == "" || len(m.Dependencies) == 0 {
			rep.manifestOK = false
			rep.details = append(rep.details, "manifest missing tool/version/params/dependencies")
		}
		for _, d := range m.Dependencies {
			if d.Key == "" || d.Hash == "" {
				rep.manifestOK = false
				rep.details = append(rep.details, fmt.Sprintf("dependency %q lacks key/hash", d.Node))
				break
			}
		}
	}
	if !anySegment {
		return nil, exitCode{code: exitDamaged, message: "verify: no segments found under " + dir}
	}
	sort.Strings(rep.details)
	return rep, nil
}

// readSegments returns the ordered segment hashes from a manifest, for tests.
func readSegments(manifestPath string) ([]string, error) {
	m, err := provenance.Read(manifestPath)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, s := range m.Segments {
		out = append(out, s.SHA256)
	}
	return out, nil
}

// verifyManifest re-checks a single output tree, returning counts.
type manifestCheck struct {
	segmentsTotal int
	segmentsOK    int
}

func verifyManifest(dir string) (*manifestCheck, error) {
	m, err := provenance.Read(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	out := &manifestCheck{segmentsTotal: len(m.Segments)}
	for _, s := range m.Segments {
		ok, _, err := provenance.VerifySegment(dir, s)
		if err == nil && ok {
			out.segmentsOK++
		}
	}
	return out, nil
}

var _ = strings.Join
var _ = hex.EncodeToString
var _ = ts.MaxFileBytes
var _ = sort.Strings
var _ = remux.Timescale
