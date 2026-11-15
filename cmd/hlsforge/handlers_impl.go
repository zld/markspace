package main

import (
	"hlsforge/internal/app"
	"hlsforge/internal/cli"
	"hlsforge/internal/report"
)

// Thin adapters keep the exit-code mapping in main.go while all pipeline
// logic lives under internal/app.

func runDemoImpl(cfg *cli.DemoConfig) (*report.Report, error) {
	return app.RunDemo(cfg)
}

func runBuildImpl(cfg *cli.BuildConfig) (*report.Report, error) {
	return app.RunBuild(cfg)
}

func runVerifyImpl(cfg *cli.VerifyConfig) (*report.Report, error) {
	return app.RunVerify(cfg)
}
