package main

import (
	"flag"
	"fmt"
	"io"

	"mediaflow/internal/probe"
)

func generateFixtures(work string, c *commonFlags) (*probe.Manifest, string, error) {
	return nil, "", fmt.Errorf("demo not implemented")
}

func loadManifest(path string) (*probe.Manifest, error) {
	return probe.LoadManifest(path)
}

func runDemo(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c := bindCommon(fs)
	_ = c
	fmt.Fprintln(stdout, "demo stub")
	return 0
}
