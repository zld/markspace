package fixture

import (
	"testing"
)

func TestParseMajorVersion(t *testing.T) {
	cases := []struct {
		banner string
		want   int
		ok     bool
	}{
		{"ffmpeg version 7.1 Copyright", 7, true},
		{"ffmpeg version 9.0.2 Copyright (c)", 9, true},
		{"ffmpeg version 10.0", 10, true},
		{"unexpected banner", 0, false},
	}
	for _, tc := range cases {
		got, ok := parseMajorVersion(tc.banner)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("parseMajorVersion(%q) = (%d,%v), want (%d,%v)",
				tc.banner, got, ok, tc.want, tc.ok)
		}
	}
}

func TestVerifyFFmpegReturnsDescriptiveErrorWhenAbsent(t *testing.T) {
	// With a deliberately empty PATH the lookup must fail with the explicit
	// missing-binary error rather than an opaque exec failure.
	t.Setenv("PATH", "/nonexistent-empty-path")
	err := VerifyFFmpeg()
	if err == nil {
		t.Fatal("expected error when ffmpeg is absent")
	}
	if err != ErrFFmpegMissing {
		t.Fatalf("err = %v, want ErrFFmpegMissing", err)
	}
}
