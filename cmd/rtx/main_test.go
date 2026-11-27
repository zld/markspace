package main

import (
	"bytes"
	"testing"
)

func TestFixtureDeterminism(t *testing.T) {
	c := parseCorrupt([]string{"tail", "truncated"})
	w1 := generateWAV(17, 2, 48000, c)
	w2 := generateWAV(17, 2, 48000, c)
	if !bytes.Equal(w1, w2) {
		t.Fatal("WAV fixture not deterministic")
	}
	r1 := generateRTP(17, 48000)
	r2 := generateRTP(17, 48000)
	if !bytes.Equal(r1, r2) {
		t.Fatal("RTP fixture not deterministic")
	}
	if !bytes.Equal(generateMP4(48000), generateMP4(48000)) {
		t.Fatal("MP4 fixture not deterministic")
	}
	other := generateRTP(18, 48000)
	if bytes.Equal(r1, other) {
		t.Fatal("different seeds must change the trace")
	}
}

func TestDemoExitCodes(t *testing.T) {
	ok := run([]string{"demo", "--seed", "17", "--channels", "2",
		"--sample-rate", "48000", "--budget-ms", "120",
		"--max-drift-ppm", "300", "--corrupt", "tail,truncated"})
	if ok != 0 {
		t.Fatalf("healthy budget must exit 0, got %d", ok)
	}
	over := run([]string{"demo", "--seed", "17", "--channels", "2",
		"--sample-rate", "48000", "--budget-ms", "1", "--max-drift-ppm", "300"})
	if over != 2 {
		t.Fatalf("exceeded budget must exit 2, got %d", over)
	}
	if code := run([]string{"bogus"}); code != 1 {
		t.Fatalf("unknown command must exit 1, got %d", code)
	}
}

func TestCorruptFlagParsing(t *testing.T) {
	c := parseCorrupt([]string{"tail", "truncated"})
	if !c.tail || !c.truncated {
		t.Fatalf("flags not parsed: %+v", c)
	}
	if parseCorrupt([]string{""}).tail {
		t.Fatal("empty corruption list must be clean")
	}
}
