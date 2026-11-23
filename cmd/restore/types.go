package main

import "mediaforensics/internal/fixture"

// caseResult carries one fixture case's acceptance metrics and the digest of
// its evidence manifest.
type caseResult struct {
	name          string
	offset        int64
	recoveredRate float64
	syncErrorMs   float64
	p95LatencyMs  float64
	exitCode      int
	manifestSHA   string
}

// damageFunc mutates a generated container and reports the applied damage.
type damageFunc func(data []byte, layout *fixture.Layout) ([]byte, fixture.Damage, error)
