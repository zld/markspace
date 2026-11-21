package config

import (
	"errors"
	"strings"
	"testing"
	"time"

	"streamcheck/internal/probe"
)

func TestParseBuildDefaults(t *testing.T) {
	cfg, err := ParseBuild([]string{"--input", "/tmp/x", "--rates", "1600,800"})
	if err != nil {
		t.Fatalf("ParseBuild: %v", err)
	}
	if len(cfg.Rates) != 2 || cfg.Rates[0] != 1600 || cfg.Rates[1] != 800 {
		t.Fatalf("rates = %v", cfg.Rates)
	}
	if cfg.ManifestDir != "/tmp/x" || cfg.ProbeTimeout != probe.DefaultTimeout ||
		cfg.Concurrency != 4 || cfg.ToleranceMS != 80 || cfg.SegmentSeconds != 4 {
		t.Fatalf("defaults = %+v", cfg)
	}
}

func TestParseBuildValidation(t *testing.T) {
	cases := [][]string{
		{},
		{"--input", "/tmp/x"},
		{"--rates", "800"},
		{"--input", "/tmp/x", "--rates", "800,800"},
		{"--input", "/tmp/x", "--rates", "800,0"},
		{"--input", "/tmp/x", "--rates", "800,-1"},
		{"--input", "/tmp/x", "--rates", "800,abc"},
		{"--input", "/tmp/x", "--rates", "800,"},
		{"--input", "/tmp/x", "--rates", "800", "--segment-seconds", "0"},
		{"--input", "/tmp/x", "--rates", "800", "--tolerance-ms", "-1"},
		{"--input", "/tmp/x", "--rates", "800", "--probe-timeout", "0s"},
		{"--input", "/tmp/x", "--rates", "800", "--concurrency", "0"},
		{"--input", "/tmp/x", "--rates", "800", "--concurrency", "65"},
		{"--input", "/tmp/x", "--rates", "800", "--pattern", "{index}_{index}.ts"},
		{"--input", "/tmp/x", "--rates", "800,1600", "--pattern", "chunk_{index}.ts"},
		{"--input", "/tmp/x", "--rates", "800", "extra"},
		{"--input", "/tmp/x", "--rates", "800", "--bogus"},
	}
	for _, args := range cases {
		_, err := ParseBuild(args)
		var usageErr *UsageError
		if !errors.As(err, &usageErr) {
			t.Errorf("ParseBuild(%v) error = %v, want UsageError", args, err)
			continue
		}
		if !strings.Contains(usageErr.Usage, "streamcheck build") {
			t.Errorf("usage missing for %v", args)
		}
	}
}

func TestParseBuildOverrides(t *testing.T) {
	cfg, err := ParseBuild([]string{
		"--input", "/in", "--rates", "800", "--pattern", "chunk-{index}.m4s",
		"--segment-seconds", "2.5", "--tolerance-ms", "0", "--strict", "--json",
		"--manifest-dir", "/out", "--ffprobe", "/opt/ffprobe",
		"--probe-timeout", "250ms", "--concurrency", "1",
	})
	if err != nil {
		t.Fatalf("ParseBuild: %v", err)
	}
	if !cfg.Strict || !cfg.JSON || cfg.Pattern != "chunk-{index}.m4s" ||
		cfg.SegmentSeconds != 2.5 || cfg.ManifestDir != "/out" ||
		cfg.FFprobe != "/opt/ffprobe" || cfg.ProbeTimeout != 250*time.Millisecond ||
		cfg.Concurrency != 1 || cfg.ToleranceMS != 0 {
		t.Fatalf("config = %+v", cfg)
	}
}
