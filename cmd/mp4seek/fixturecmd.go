package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"mp4seek/internal/fixture"
)

func cmdFixture(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
	fs.SetOutput(stderr)
	profile := fs.String("profile", "", "fixture profile: edit-wrap, fragmented-wrap, truncated, oob")
	out := fs.String("out", "", "output file path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *profile == "" || *out == "" {
		fs.Usage()
		return fmt.Errorf("fixture: --profile and --out are required")
	}
	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	if err := fixture.Build(*profile, f); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "wrote profile=%s out=%s\n", *profile, *out)
	return err
}
