package main

import (
	"encoding/json"
	"flag"
	"fmt"

	"mediapack/internal/batch"
	"mediapack/internal/metrics"
)

func runVerify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	dir := fs.String("dir", "./tmp/demo", "demo/batch output directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	report, _, err := metrics.VerifyDirectory(*dir)
	if report != nil {
		b, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(b))
	}
	return exitError(err)
}

func runRecover(args []string) int {
	fs := flag.NewFlagSet("recover", flag.ContinueOnError)
	dir := fs.String("dir", "./tmp/demo", "batch output directory containing project.json")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := batch.LoadConfig(*dir)
	if err != nil {
		return exitError(err)
	}
	res, err := batch.Run(cfg)
	if err != nil {
		return exitError(err)
	}
	b, _ := json.MarshalIndent(map[string]any{
		"out":              cfg.OutDir,
		"committed_chunks": res.Manifest.CommittedChunks,
		"recovered_chunks": res.Manifest.RecoveredChunks,
		"samples":          res.Manifest.SampleCount,
		"manifest":         "manifest.json",
	}, "", "  ")
	fmt.Println(string(b))
	return 0
}
