package main

import (
	"fmt"
	"os"
	"strconv"

	"mediasync/internal/fixture"
	"mediasync/internal/pipeline"
)

func cmdDemo(args []string) int {
	flags, code := parseFlags(args, map[string]bool{"seed": true, "out": true, "case": true})
	if code != 0 {
		return code
	}
	seedStr, ok := flags["seed"]
	var seed int64
	if !ok {
		seed = 1 // deterministic default so truncated runs are reproducible
	} else {
		parsed, err := strconv.ParseInt(seedStr, 10, 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid --seed %q: %v\n", seedStr, err)
			return 2
		}
		seed = parsed
	}
	out, ok := flags["out"]
	if !ok || out == "" {
		fmt.Fprintln(os.Stderr, "demo requires --out")
		return 2
	}
	fault := fixture.FaultNone
	if c, ok := flags["case"]; ok {
		switch c {
		case "truncated":
			fault = fixture.FaultTruncated
		case "", "ok", "success":
			fault = fixture.FaultNone
		default:
			fmt.Fprintf(os.Stderr, "unknown --case %q (want \"truncated\")\n", c)
			return 2
		}
	}

	fx, err := fixture.Generate(seed, fault)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fixture generation failed: %v\n", err)
		return 1
	}
	// Persist the full, uncut fixture (the fault is a run-time injection) so
	// recover/verify can always rebuild from pristine inputs.
	clean, err := fixture.Generate(seed, fixture.FaultNone)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fixture generation failed: %v\n", err)
		return 1
	}
	if err := pipeline.PersistFixture(out, clean); err != nil {
		fmt.Fprintf(os.Stderr, "persist fixture failed: %v\n", err)
		return 1
	}

	p, err := pipeline.New(pipeline.DefaultConfig(seed), out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pipeline init failed: %v\n", err)
		return 1
	}
	res, err := p.Run(fx)
	p.Close()
	if err != nil {
		// Processing failure: report the located fault and exit 1.
		fmt.Printf("demo FAILED at stage=%s\n", res.FailedStage)
		if res.Failure != nil {
			fmt.Printf("truncation/fault: code=%s track=%s offset=%d\n",
				res.Failure.Code, res.Failure.Track, res.Failure.Offset)
		}
		fmt.Printf("error: %v\n", err)
		return 1
	}

	printSummary(res)
	fmt.Printf("summary hash: %s\n", res.SummaryHash)
	fmt.Println("demo OK")
	return 0
}

func cmdVerify(args []string) int {
	flags, code := parseFlags(args, map[string]bool{"dir": true})
	if code != 0 {
		return code
	}
	dir, ok := flags["dir"]
	if !ok || dir == "" {
		fmt.Fprintln(os.Stderr, "verify requires --dir")
		return 2
	}
	// First reconcile any interrupted state, then recompute and compare.
	if _, err := pipeline.Recover(dir); err != nil {
		fmt.Fprintf(os.Stderr, "recovery check failed: %v\n", err)
		return 1
	}
	if err := pipeline.Verify(dir, 1); err != nil {
		fmt.Fprintf(os.Stderr, "verify FAILED: %v\n", err)
		return 1
	}
	fmt.Println("verify OK: hashes identical, alignment error within 1 ms")
	return 0
}

func cmdRecover(args []string) int {
	flags, code := parseFlags(args, map[string]bool{"dir": true})
	if code != 0 {
		return code
	}
	dir, ok := flags["dir"]
	if !ok || dir == "" {
		fmt.Fprintln(os.Stderr, "recover requires --dir")
		return 2
	}
	done, err := pipeline.Recover(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "recovery failed: %v\n", err)
		return 1
	}
	fmt.Printf("recovered committed stages before resume: %v\n", done)

	// Resume from the persisted full fixture and run to completion.
	fx, err := pipeline.LoadFixture(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load fixture failed: %v\n", err)
		return 1
	}
	p, err := pipeline.New(pipeline.DefaultConfig(fx.Seed), dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pipeline init failed: %v\n", err)
		return 1
	}
	res, err := p.Run(fx)
	p.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "resumed run failed: %v\n", err)
		return 1
	}
	printSummary(res)
	fmt.Printf("manifest/summary hash: %s\n", res.SummaryHash)
	fmt.Println("recover OK")
	return 0
}
