package fault

import (
	"errors"
	"testing"
)

func TestErrorContainsOffsetTrackAndRecovery(t *testing.T) {
	err := Track(KindTruncated, "a.mp4", 7, 1234, errors.New("EOF"), true)
	if err.Offset != 1234 || err.TrackID != 7 || !err.Recoverable {
		t.Fatalf("bad structured error: %+v", err)
	}
	if got := err.Error(); got == "" || !contains(got, "offset=1234") || !contains(got, "track=7") {
		t.Fatalf("message missing context: %s", got)
	}
	fe, ok := As(err)
	if !ok || fe.Kind != KindTruncated {
		t.Fatalf("As failed: %#v", fe)
	}
	if !errors.Is(err, err.Err) {
		t.Fatal("unwrap does not expose cause")
	}
}

func TestExitCode(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{nil, 0},
		{New(KindArgument, "bad"), 2},
		{New(KindMapping, "missing"), 2},
		{New(KindPath, "escape"), 2},
		{New(KindUnrecoverable, "gone"), 3},
		{New(KindTruncated, "partial"), 3},
		{New(KindVerify, "mismatch"), 1},
	}
	for _, tc := range cases {
		if got := ExitCode(tc.err); got != tc.want {
			t.Fatalf("ExitCode(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
