package main

import (
	"context"
	"flag"
	"fmt"

	"pwflow/internal/pipeline"
	"pwflow/internal/validate"
)

func runVerifyCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	manifest := fs.String("manifest", "", "path to manifest.json")
	if err := fs.Parse(args); err != nil {
		return &cliError{code: exitRejected, err: err}
	}
	if *manifest == "" {
		return &cliError{code: exitRejected, err: fmt.Errorf("--manifest is required")}
	}
	rep, err := validate.Verify(ctx, *manifest)
	if err != nil {
		if rep != nil {
			printVerification(rep)
		}
		return &cliError{code: exitRejected, err: err}
	}
	printManifestSummary(rep.Manifest, rep.ManifestSHA)
	printVerification(rep)
	if rep.Status == pipeline.StatusGaps {
		return &cliError{code: exitGaps, err: fmt.Errorf("verification completed with recoverable gaps")}
	}
	return nil
}

func printVerification(rep *validate.Report) {
	passed := 0
	for _, c := range rep.Checks {
		if c.OK {
			passed++
		}
	}
	fmt.Printf("校验状态: %s (%d/%d checks passed)\n", rep.Status, passed, len(rep.Checks))
	for _, f := range rep.Failures {
		fmt.Printf("  FAIL: %s\n", f)
	}
}
