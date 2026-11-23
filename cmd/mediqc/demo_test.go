package main

import "testing"

// TestDemoEndToEnd runs the full pipeline with resume check enabled.
func TestDemoEndToEnd(t *testing.T) {
	cfg := demoConfig{
		seed:          42,
		resumeCheck:   true,
		slowSinkMs:    1,
		maxBufferByte: 256 * 1024,
	}
	if err := runDemo(cfg); err != nil {
		t.Fatal(err)
	}
}

// TestDemoSeeds verifies determinism across several seeds.
func TestDemoSeeds(t *testing.T) {
	for _, seed := range []int64{1, 7, 99} {
		cfg := demoConfig{seed: seed, resumeCheck: true, maxBufferByte: 256 * 1024}
		if err := runDemo(cfg); err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
	}
}
