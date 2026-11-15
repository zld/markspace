package main

import (
	"hlsforge/internal/cli"
	"hlsforge/internal/report"
)

// The three subcommand handlers live in separate files of package main and
// are wired in as the pipeline packages land. They always return a populated
// report so the exit-code mapping in main.go stays uniform.

func runDemo(cfg *cli.DemoConfig) (*report.Report, error) {
	return runDemoImpl(cfg)
}

func runBuild(cfg *cli.BuildConfig) (*report.Report, error) {
	return runBuildImpl(cfg)
}

func runVerify(cfg *cli.VerifyConfig) (*report.Report, error) {
	return runVerifyImpl(cfg)
}
