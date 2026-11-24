package model

import (
	"errors"
	"math"
	"testing"
)

func TestToNSZeroTimescale(t *testing.T) {
	_, err := ToNS(123, 0)
	if !errors.Is(err, ErrZeroTimescale) {
		t.Fatalf("err=%v", err)
	}
}

func TestToNSOverflow(t *testing.T) {
	_, err := ToNS(math.MaxUint64, 2)
	var overflow *OverflowError
	if !errors.As(err, &overflow) {
		t.Fatalf("err=%v want OverflowError", err)
	}
}

func TestExitCodes(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{nil, 0},
		{&ConfigError{Field: "x"}, 3},
		{&ParseError{ByteOffset: 7}, 2},
		{&IntegrityError{}, 2},
	}
	for _, tc := range cases {
		if got := ExitCode(tc.err); got != tc.want {
			t.Fatalf("ExitCode(%v)=%d want %d", tc.err, got, tc.want)
		}
	}
}
