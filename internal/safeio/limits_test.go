package safeio

import (
	"errors"
	"io"
	"testing"

	"mediacheck/internal/model"
)

func TestResolveContained(t *testing.T) {
	root := t.TempDir()
	good := []string{"a/b.m4s", "seg/../a.m4s", "playlist.m3u8"}
	for _, name := range good {
		if _, err := ResolveContained(root, name); err != nil {
			t.Errorf("ResolveContained(%q): %v", name, err)
		}
	}
	bad := []string{"", "/etc/passwd", "../escape.m4s", "../../x", "a/../../b"}
	for _, name := range bad {
		if _, err := ResolveContained(root, name); !errors.Is(err, model.ErrUnsafePath) {
			t.Errorf("ResolveContained(%q) = %v, want unsafe", name, err)
		}
	}
}

func TestLimitedReader(t *testing.T) {
	r := LimitedReader(io.LimitReader(zeroReader{}, 100), 4)
	buf := make([]byte, 10)
	n, err := r.Read(buf)
	if n != 4 || !errors.Is(err, model.ErrMemoryLimit) {
		t.Fatalf("got n=%d err=%v", n, err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

func TestCheckDuration(t *testing.T) {
	lim := DefaultLimits()
	if err := CheckDuration(90000, lim); err != nil {
		t.Fatalf("valid duration rejected: %v", err)
	}
	for _, d := range []int64{0, -1, lim.MaxDurationTick + 1} {
		if err := CheckDuration(d, lim); err == nil {
			t.Errorf("duration %d should be rejected", d)
		}
	}
}
