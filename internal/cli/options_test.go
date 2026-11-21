package cli

import (
	"errors"
	"testing"
	"time"
)

func TestParseDemoDefaults(t *testing.T) {
	opts, err := Parse([]string{"demo"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Command != CommandDemo {
		t.Fatalf("command=%q", opts.Command)
	}
	if opts.Listen != DefaultListen {
		t.Fatalf("listen=%q", opts.Listen)
	}
	if opts.Packets != DefaultPackets || opts.Interval != DefaultInterval {
		t.Fatalf("unexpected defaults: %+v", opts)
	}
}

func TestParseAcceptanceCommand(t *testing.T) {
	opts, err := Parse([]string{
		"demo", "--listen", "127.0.0.1:0", "--packets", "240",
		"--interval", "20ms", "--drop-every", "37",
		"--delay-every", "29", "--delay", "7ms", "--json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Packets != 240 || opts.DropEvery != 37 || opts.DelayEvery != 29 {
		t.Fatalf("options=%+v", opts)
	}
	if opts.Delay != 7*time.Millisecond || !opts.JSON {
		t.Fatalf("options=%+v", opts)
	}
}

func TestParseRejectsInvalidOptions(t *testing.T) {
	cases := [][]string{
		{},
		{"widget"},
		{"demo", "--listen", "0.0.0.0:9000"},
		{"demo", "--listen", "127.0.0.1"},
		{"demo", "--listen", "127.0.0.1:70000"},
		{"demo", "--listen", "example.com:9000"},
		{"demo", "--packets", "0"},
		{"demo", "--packets", "200000"},
		{"demo", "--interval", "0s"},
		{"demo", "--drop-every", "1"},
		{"demo", "--drop-every", "500"},
		{"demo", "--delay-every", "1"},
		{"demo", "--delay-every", "10"},
		{"demo", "--delay", "-1ms"},
		{"demo", "--timeout", "10ms"},
		{"demo", "--queue-size", "0"},
		{"demo", "extra"},
		{"demo", "--unknown"},
	}
	for _, args := range cases {
		if _, err := Parse(args); err == nil || errors.Is(err, ErrHelp) {
			t.Fatalf("expected error for %v, got %v", args, err)
		}
	}
}

func TestParseHelp(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}} {
		if _, err := Parse(args); !errors.Is(err, ErrHelp) {
			t.Fatalf("args %v: err=%v", args, err)
		}
	}
}

func TestValidateLoopbackVariants(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:0", "127.5.5.5:1234", "[::1]:0"} {
		opts := Defaults()
		opts.Listen = addr
		if err := opts.Validate(); err != nil {
			t.Fatalf("%s rejected: %v", addr, err)
		}
	}
}
