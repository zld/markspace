package internal

import (
	"errors"
	"math"
	"testing"
)

func TestParseDecimalSeconds(t *testing.T) {
	cases := []struct {
		in   string
		want NS
	}{
		{"0", 0},
		{"2", 2 * NSPerSecond},
		{"2.5", 2500 * NSPerMillisecond},
		{"4.123456789", 4*NSPerSecond + 123456789},
		{"0.0000000005", 1}, // rounds to one nanosecond
	}
	for _, tc := range cases {
		got, err := ParseDecimalSeconds(tc.in)
		if err != nil {
			t.Fatalf("ParseDecimalSeconds(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ParseDecimalSeconds(%q)=%d, want %d", tc.in, got, tc.want)
		}
	}

	for _, in := range []string{"", "abc", "1.2.3", "999999999999999999999"} {
		if _, err := ParseDecimalSeconds(in); err == nil {
			t.Fatalf("expected error for %q", in)
		}
	}
}

func TestScaledToNSOverflowAndTimescale(t *testing.T) {
	got, err := ScaledToNS(48000, 48000)
	if err != nil || got != NSPerSecond {
		t.Fatalf("ScaledToNS()=(%d,%v)", got, err)
	}
	if _, err := ScaledToNS(1, 0); err == nil {
		t.Fatal("expected timescale error")
	}
	if _, err := ScaledToNS(math.MaxInt64, 1); err == nil {
		t.Fatal("expected overflow error")
	}
}

func TestFaultWrapping(t *testing.T) {
	base := NewFault("hls.parse", "a.m3u8", 42, "bad %s", "extinf")
	wrapped := WrapFault("pipe.process", "", 0, base)
	if !errors.Is(wrapped, base) {
		t.Fatal("wrapped fault does not unwrap to base")
	}
	f, ok := AsFault(wrapped)
	if !ok || f.Source != "a.m3u8" || f.Offset != 42 {
		t.Fatalf("source location not preserved: %#v ok=%v", f, ok)
	}
}
