package config

import "testing"

func TestParseFixture(t *testing.T) {
	cfg, err := ParseFixture([]string{"--out", "/tmp/f"})
	if err != nil {
		t.Fatalf("ParseFixture: %v", err)
	}
	if cfg.Segments != 6 || cfg.SegmentSeconds != 4 || len(cfg.Rates) != 2 {
		t.Fatalf("fixture config = %+v", cfg)
	}
	for _, args := range [][]string{
		{}, {"--out", "/tmp/f", "--segments", "0"}, {"--out", "/tmp/f", "--rates", "800,800"},
	} {
		if _, err := ParseFixture(args); err == nil {
			t.Errorf("ParseFixture(%v) succeeded, want error", args)
		}
	}
}
