// Command replay is the thin terminal front end for the replaykit media
// kernel. It generates deterministic sample inputs, runs the selftest
// scenarios and prints the verification counters. Exit status:
//
//	0  selftest passed (including expected/recovered fault cases)
//	1  input fatal error or output verification/selftest failure
//	2  an unrecoverable media interval remains (manifest retained)
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"replaykit/internal/fixture"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(0)
	}
	switch os.Args[1] {
	case "selftest":
		os.Exit(runSelftest(os.Args[2:]))
	case "generate":
		os.Exit(runGenerate(os.Args[2:]))
	case "help", "-h", "--help":
		usage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "replay: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `replay - deterministic replay clipping kernel

usage:
  replay selftest [--case all|normal|audiofault|truncated|badindex|corruptskip|resume|verifyfail|unrecoverable|fatal]
                  [--out DIR]
  replay generate [--out DIR]

The acceptance entry point is:
  go run ./cmd/replay selftest --case all --out /tmp/replay-proof
`)
}

var caseOrder = []string{
	"normal", "audiofault", "truncated", "badindex",
	"corruptskip", "resume", "verifyfail", "unrecoverable", "fatal",
}

func caseFuncs() map[string]func(string) scenarioResult {
	return map[string]func(string) scenarioResult{
		"normal":        runNormal,
		"audiofault":    runAudioFault,
		"truncated":     runTruncated,
		"badindex":      runBadIndex,
		"corruptskip":   runCorruptSkip,
		"resume":        runResume,
		"verifyfail":    runVerifyFail,
		"unrecoverable": runUnrecoverable,
		"fatal":         runFatal,
	}
}

func runSelftest(args []string) int {
	fs := flag.NewFlagSet("selftest", flag.ContinueOnError)
	caseName := fs.String("case", "all", "selftest case to run (or all)")
	out := fs.String("out", filepath.Join(os.TempDir(), "replay-proof"), "output directory")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 1
	}

	if *caseName != "all" {
		fn, ok := caseFuncs()[*caseName]
		if !ok {
			fmt.Fprintf(os.Stderr, "unknown --case %q\n", *caseName)
			return 1
		}
		_ = os.RemoveAll(filepath.Join(*out, *caseName))
		r := fn(*out)
		emit(r)
		if !r.OK {
			fmt.Println("SELFTEST FAIL")
			return 1
		}
		return r.ExpectedCode
	}

	_, allOK, err := executeAll(*out, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	if !allOK {
		fmt.Println("SELFTEST FAIL (exit 1)")
		return 1
	}
	fmt.Println("SELFTEST PASS (exit 0)")
	return 0
}

// executeAll regenerates fixtures, runs every scenario and writes proof.json.
// When verbose is true each scenario line and the PROOF block are printed.
// It returns the canonical proof and whether every scenario met expectations.
func executeAll(out string, verbose bool) (Proof, bool, error) {
	if written, err := fixture.WriteSampleTree(filepath.Join(out, "fixtures")); err == nil {
		if verbose {
			fmt.Printf("[fixture] generated %d sample files under %s/fixtures\n", len(written), out)
		}
	} else if verbose {
		fmt.Printf("[fixture] sample tree write warning: %v\n", err)
	}

	funcs := caseFuncs()
	results := make(map[string]scenarioResult, len(caseOrder))
	allOK := true
	for _, name := range caseOrder {
		_ = os.RemoveAll(filepath.Join(out, name))
		r := funcs[name](out)
		results[name] = r
		if verbose {
			emit(r)
		}
		if !r.OK || r.Code != r.ExpectedCode {
			allOK = false
		}
	}

	n, a, t, i, rs := results["normal"], results["audiofault"], results["truncated"],
		results["badindex"], results["resume"]
	p := Proof{
		Cases:             orderedResults(results),
		Roundtrip:         fmt.Sprintf("%d%%", n.Metrics.RoundtripPct),
		IndexHits:         fmt.Sprintf("%d%%", n.Metrics.IndexHitsPct),
		BoundarySkewTicks: n.Metrics.BoundarySkewTicks,
		Segments:          n.Metrics.Segments,
		HashMatch:         hashMatchFrac(n.Metrics.HashMatched, n.Metrics.HashTotal),
		Anomalies:         n.Metrics.Anomalies,
		Lost:              a.Metrics.Lost,
		Duplicate:         a.Metrics.Duplicate,
		Truncated:         t.Metrics.Truncated,
		IndexRebuilt:      i.Metrics.IndexRebuilt,
		Rebuilt:           rs.Metrics.Rebuilt,
		Reused:            rs.Metrics.Reused,
		DuplicateProducts: rs.Metrics.DuplicateProducts,
		ResumeHashMatch:   hashMatchFrac(rs.Metrics.HashMatched, rs.Metrics.HashTotal),
	}
	if allOK {
		p.Status, p.ExitCode = "pass", 0
	} else {
		p.Status, p.ExitCode = "fail", 1
	}
	if err := writeProof(out, p); err != nil {
		return p, false, fmt.Errorf("write proof: %w", err)
	}
	if verbose {
		fmt.Printf("PROOF normal roundtrip=%s index_hits=%s boundary_skew_ticks=%d segments=%d hash_match=%s anomalies=%d\n",
			p.Roundtrip, p.IndexHits, p.BoundarySkewTicks, p.Segments, p.HashMatch, p.Anomalies)
		fmt.Printf("PROOF faults lost=%d duplicate=%d truncated=%d index_rebuilt=%d\n",
			p.Lost, p.Duplicate, p.Truncated, p.IndexRebuilt)
		fmt.Printf("PROOF resume rebuilt=%d reused=%d duplicate_products=%d hash_match=%s\n",
			p.Rebuilt, p.Reused, p.DuplicateProducts, p.ResumeHashMatch)
		fmt.Printf("proof written: %s\n", filepath.Join(out, "proof.json"))
	}
	return p, allOK, nil
}

func orderedResults(m map[string]scenarioResult) []scenarioResult {
	out := make([]scenarioResult, 0, len(caseOrder))
	for _, name := range caseOrder {
		if r, ok := m[name]; ok {
			out = append(out, r)
		}
	}
	return out
}

func runGenerate(args []string) int {
	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	out := fs.String("out", filepath.Join(os.TempDir(), "replay-samples"), "directory for generated samples")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 1
	}
	written, err := fixture.WriteSampleTree(*out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate: %v\n", err)
		return 1
	}
	for _, rel := range written {
		fmt.Println(rel)
	}
	fmt.Printf("generated %d files in %s\n", len(written), *out)
	return 0
}
