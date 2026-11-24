package main

import "testing"

func TestUsageExitCodes(t *testing.T) {
	if code := run([]string{"no-such-command"}); code != 2 {
		t.Fatalf("unknown command code=%d, want 2", code)
	}
	if code := run([]string{"verify", "--dir", t.TempDir()}); code != 3 {
		t.Fatalf("verify without manifest code=%d, want 3", code)
	}
	if code := run([]string{"recover", "--dir", t.TempDir()}); code != 3 {
		t.Fatalf("recover without project code=%d, want 3", code)
	}
}
