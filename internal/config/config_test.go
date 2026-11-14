package config

import (
	"testing"
	"time"
)

func valid() Config {
	c := Default()
	_ = c.Validate() // fills MaxrateKbps default
	return c
}

func TestDefaultValid(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatalf("default config invalid: %v", err)
	}
	if c.MaxrateKbps != 2*c.TargetKbps {
		t.Fatalf("maxrate default = %d, want %d", c.MaxrateKbps, 2*c.TargetKbps)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"empty outdir", func(c *Config) { c.OutDir = "" }},
		{"zero target", func(c *Config) { c.TargetKbps = 0 }},
		{"negative target", func(c *Config) { c.TargetKbps = -100 }},
		{"maxrate below target", func(c *Config) { c.MaxrateKbps = c.TargetKbps - 1 }},
		{"zero segment", func(c *Config) { c.Segment = 0 }},
		{"negative segment", func(c *Config) { c.Segment = -time.Second }},
		{"huge segment", func(c *Config) { c.Segment = 2 * time.Hour }},
		{"zero concurrency", func(c *Config) { c.Concurrency = 0 }},
		{"excess concurrency", func(c *Config) { c.Concurrency = MaxConcurrency + 1 }},
		{"negative retries", func(c *Config) { c.Retries = -1 }},
		{"excess retries", func(c *Config) { c.Retries = MaxRetries + 1 }},
		{"tiny timeout", func(c *Config) { c.Timeout = time.Millisecond }},
		{"zero fps", func(c *Config) { c.FPS = 0 }},
		{"bad pixfmt", func(c *Config) { c.PixFmt = "rgb24" }},
		{"bad preset", func(c *Config) { c.Preset = "ludicrous" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := valid()
			tc.mutate(&c)
			if err := c.Validate(); err == nil {
				t.Fatalf("expected validation error for %s", tc.name)
			}
		})
	}
}
