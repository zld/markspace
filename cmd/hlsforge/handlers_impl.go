package main

import (
	"errors"

	"hlsforge/internal/cli"
	"hlsforge/internal/report"
)

// Temporary stubs. Each is replaced by the real pipeline when its package
// lands, keeping every commit in a compilable state.

func runDemoImpl(cfg *cli.DemoConfig) (*report.Report, error) {
	return nil, errors.New("demo pipeline not yet implemented")
}

func runBuildImpl(cfg *cli.BuildConfig) (*report.Report, error) {
	return nil, errors.New("build pipeline not yet implemented")
}

func runVerifyImpl(cfg *cli.VerifyConfig) (*report.Report, error) {
	return nil, errors.New("verify pipeline not yet implemented")
}
