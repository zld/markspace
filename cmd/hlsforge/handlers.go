package main

import (
	"hlsforge/internal/app"
	"hlsforge/internal/cli"
	"hlsforge/internal/report"
)

// The subcommand handlers are thin adapters over internal/app so the
// exit-code mapping in main.go stays uniform and the pipeline logic is shared
// with the package-level tests.

func runDemo(cfg *cli.DemoConfig) (*report.Report, error) {
	return app.RunDemo(cfg)
}

func runBuild(cfg *cli.BuildConfig) (*report.Report, error) {
	return app.RunBuild(cfg)
}

func runVerify(cfg *cli.VerifyConfig) (*report.Report, error) {
	return app.RunVerify(cfg)
}
