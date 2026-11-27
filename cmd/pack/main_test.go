package main

import (
	"testing"

	"gamepack/internal/fixture"
	perr "gamepack/internal/errors"
	"gamepack/internal/pipeline"
	"gamepack/internal/stream"
	"gamepack/internal/txn"
)

func TestParseFlags(t *testing.T) {
	f, err := parseFlags([]string{
		"--root", "/tmp/x", "--codecs", "h264,av1",
		"--bitrates", "500,1000", "--inject", "switch,wrap",
		"--crash-after", "align",
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.root != "/tmp/x" || len(f.codecs) != 2 || len(f.bitrates) != 2 {
		t.Fatalf("parsed flags wrong: %+v", f)
	}
	if !f.inject[fixture.InjectSwitch] || !f.inject[fixture.InjectWrap] {
		t.Fatal("injections not parsed")
	}
	if f.crashAfter != txn.StageAlign {
		t.Fatalf("crash stage = %q", f.crashAfter)
	}
}

func TestParseFlagsDefaultsAndErrors(t *testing.T) {
	if _, err := parseFlags([]string{}); err == nil {
		t.Fatal("missing --root must error")
	}
	if _, err := parseFlags([]string{"--root", "/x", "--codecs", "ogg"}); err == nil {
		t.Fatal("unsupported codec must error")
	}
	if _, err := parseFlags([]string{"--root", "/x", "--inject", "bogus"}); err == nil {
		t.Fatal("unknown injection must error")
	}
	f, err := parseFlags([]string{"--root", "/x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.codecs) != 1 || f.codecs[0] != stream.CodecH264 {
		t.Fatal("default codec should be h264")
	}
}

func TestExitCodeMapping(t *testing.T) {
	cases := []struct {
		code perr.Code
		want int
	}{
		{perr.CodeTruncatedUnit, 2},
		{perr.CodeOffsetOutOfRange, 2},
		{perr.CodeBoundaryConflict, 2},
		{perr.CodeClockBackward, 2},
		{perr.CodeChecksum, 3},
		{perr.CodeStage, 1},
		{perr.CodeTxn, 1},
	}
	for _, c := range cases {
		err := perr.New("test", c.code, "boom")
		if got := exitCode(err); got != c.want {
			t.Fatalf("code %s -> exit %d, want %d", c.code, got, c.want)
		}
	}
}

func TestExitCodeSimulatedCrash(t *testing.T) {
	if got := exitCode(&pipeline.SimulatedCrash{Stage: txn.StageMux}); got != 4 {
		t.Fatalf("crash exit = %d, want 4", got)
	}
}

func TestConfigFromRoot(t *testing.T) {
	root := t.TempDir()
	// Run a real job so the inputs directory is populated, then reconstruct.
	cfg := pipeline.Config{
		Root: root, Codecs: []stream.Codec{stream.CodecH264, stream.CodecAV1},
		Bitrates: []int{500, 900}, Units: 8, KeyEvery: 4, WrapBits: 16,
		Inject: map[fixture.Injection]bool{},
	}
	r, err := pipeline.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(); err != nil {
		t.Fatal(err)
	}
	got, err := configFromRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Codecs) != 2 || len(got.Bitrates) != 2 {
		t.Fatalf("reconstructed config = %+v", got)
	}
}
