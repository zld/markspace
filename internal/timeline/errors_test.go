package timeline

import (
	"errors"
	"fmt"
	"testing"
)

func TestMediaErrorFormatAndExitCode(t *testing.T) {
	e := New(ErrBoxTruncated, "frag.m4s", 120, "mdat payload cut short")
	if got := e.Error(); got != "E_BOX_TRUNCATED frag.m4s@120: mdat payload cut short" {
		t.Fatalf("unexpected error text: %q", got)
	}
	if ExitCode(e) != 2 {
		t.Fatalf("input error must map to exit 2, got %d", ExitCode(e))
	}

	r := New(ErrRecovery, "wal.log", -1, "input digest mismatch")
	if ExitCode(r) != 3 {
		t.Fatalf("recovery error must map to exit 3, got %d", ExitCode(r))
	}
	if ExitCode(nil) != 0 {
		t.Fatal("nil error must map to exit 0")
	}
}

func TestAsMediaErrorWrapping(t *testing.T) {
	inner := New(ErrSeqGap, "g.m4s", -1, "missing sequence 3")
	wrapped := fmt.Errorf("group aborted: %w", inner)
	me := AsMediaError(wrapped)
	if me == nil || me.Code != ErrSeqGap {
		t.Fatalf("wrapped MediaError not recovered: %+v", me)
	}
	plain := AsMediaError(errors.New("boom"))
	if plain.Code != ErrInvalidInput {
		t.Fatalf("plain error must be classified as invalid input, got %s", plain.Code)
	}
	if AsMediaError(nil) != nil {
		t.Fatal("nil must stay nil")
	}
}
