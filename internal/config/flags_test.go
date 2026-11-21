package config

import (
	"testing"
	"time"
)

func TestParseValid(t *testing.T) {
	f, err := Parse([]string{"rtspcheck", "demo",
		"--transport", "udp", "--frames", "24", "--seed", "7",
		"--timeout", "5s", "--format", "json"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Command != "demo" || f.Transport != TransportUDP || f.Frames != 24 ||
		f.Seed != 7 || f.Timeout != 5*time.Second || f.Format != "json" {
		t.Fatalf("parsed flags wrong: %+v", f)
	}
}

func TestParseDefaults(t *testing.T) {
	f, err := Parse([]string{"rtspcheck", "demo"})
	if err != nil {
		t.Fatalf("defaults must be valid: %v", err)
	}
	if f.Transport != TransportBoth || f.Frames != 12 || f.Format != "json" {
		t.Fatalf("unexpected defaults: %+v", f)
	}
	got := f.Transports()
	if len(got) != 2 || got[0] != TransportUDP || got[1] != TransportTCP {
		t.Fatalf("both must expand to ordered [udp,tcp], got %v", got)
	}
}

func TestParseInvalid(t *testing.T) {
	cases := [][]string{
		{"rtspcheck"},
		{"rtspcheck", "record"},
		{"rtspcheck", "demo", "--transport", "sctp"},
		{"rtspcheck", "demo", "--frames", "0"},
		{"rtspcheck", "demo", "--frames", "9999"},
		{"rtspcheck", "demo", "--seed", "-1"},
		{"rtspcheck", "demo", "--timeout", "-1s"},
		{"rtspcheck", "demo", "--format", "xml"},
		{"rtspcheck", "demo", "--unknown-flag", "1"},
	}
	for i, args := range cases {
		if _, err := Parse(args); err == nil {
			t.Fatalf("case %d %v: expected error", i, args)
		}
	}
}

func TestTransportsSingle(t *testing.T) {
	for _, tr := range []string{TransportUDP, TransportTCP} {
		f, err := Parse([]string{"rtspcheck", "demo", "--transport", tr})
		if err != nil {
			t.Fatal(err)
		}
		if got := f.Transports(); len(got) != 1 || got[0] != tr {
			t.Fatalf("transport %s expanded to %v", tr, got)
		}
	}
}

func TestFaultValidation(t *testing.T) {
	for _, fault := range []string{FaultNone, FaultBadSDP, FaultSetupFail} {
		if _, err := Parse([]string{"rtspcheck", "demo", "--fault", fault}); err != nil {
			t.Fatalf("fault %q should be valid: %v", fault, err)
		}
	}
	if _, err := Parse([]string{"rtspcheck", "demo", "--fault", "explode"}); err == nil {
		t.Fatal("invalid fault must be rejected")
	}
}

func TestDefaultFaultIsNone(t *testing.T) {
	f, err := Parse([]string{"rtspcheck", "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Fault != FaultNone {
		t.Fatalf("default fault = %q, want %q", f.Fault, FaultNone)
	}
}
