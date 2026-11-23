package main

import (
	"flag"
	"fmt"
	"os"

	"mediad/internal/kernel"
)

func runVerify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	manifest := fs.String("manifest", "", "manifest JSON to verify")
	input := fs.String("input", "", "source capture the manifest refers to")
	prov := fs.String("provenance", "", "provenance record for the manifest")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *manifest == "" || *input == "" || *prov == "" {
		fmt.Fprintln(os.Stderr, "verify requires --manifest, --input and --provenance")
		return 2
	}
	if err := kernel.VerifyFiles(*manifest, *input, *prov); err != nil {
		fmt.Fprintf(os.Stderr, "mediad verify: %v\n", err)
		code := kernel.CodeOf(err)
		if code == kernel.ExitOK {
			code = kernel.ExitInternal
		}
		return int(code)
	}
	fmt.Println("provenance_verified=true")
	return 0
}
