package config

import (
	"errors"
	"flag"
	"testing"
	"time"
)

func TestParseValid(t *testing.T) {
	cfg, err := Parse([]string{
		"--transport", "tcp", "--seed", "7", "--window", "16",
		"--timeout", "50ms", "--json",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Transport != TransportTCP || cfg.Seed != 7 || cfg.Window != 16 ||
		cfg.Timeout != 50*time.Millisecond || !cfg.JSONOutput {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestParseInvalid(t *testing.T) {
	cases := [][]string{
		{"--transport", "sctp"},
		{"--window", "1"},
		{"--window", "999999"},
		{"--timeout", "0s"},
		{"--timeout", "11s"},
		{"--timeout", "not-a-duration"},
		{"--max-packet-size", "8"},
		{"positional"},
	}
	for _, args := range cases {
		if _, err := Parse(args); err == nil {
			t.Errorf("Parse(%v) expected error, got nil", args)
		}
	}
}

func TestParseHelp(t *testing.T) {
	if _, err := Parse([]string{"-h"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("expected flag.ErrHelp, got %v", err)
	}
}

func TestValidateBoundaries(t *testing.T) {
	base := Config{
		Transport: TransportUDP, Window: MinWindowSize,
		Timeout: MinTimeout, MaxPacketSize: DefaultPacketSize,
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("minimum valid values rejected: %v", err)
	}
	base.Window = MaxWindowSize
	base.Timeout = MaxTimeout
	base.MaxPacketSize = MaxPacketSize
	if err := base.Validate(); err != nil {
		t.Fatalf("maximum valid values rejected: %v", err)
	}
}
