package errs

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorFormatting(t *testing.T) {
	e := At(Recoverable, CodeTruncatedStream, 4096, "dangling start code")
	if e.Error() != "recoverable/truncated_stream at offset 4096: dangling start code" {
		t.Fatalf("unexpected message %q", e.Error())
	}
	e2 := New(Fatal, CodeIOFailure, "checkpoint write failed")
	if e2.Offset != -1 || e2.Error() != "fatal/io_failure: checkpoint write failed" {
		t.Fatalf("unexpected message %q", e2.Error())
	}
	if Class(99).String() != "unknown" {
		t.Fatal("unknown class string mismatch")
	}
}

func TestAsWrapsPlainErrors(t *testing.T) {
	e := As(fmt.Errorf("plain failure"))
	if e == nil || e.Class != Fatal || e.Code != CodeInternal {
		t.Fatalf("plain error not normalized: %+v", e)
	}
	if As(nil) != nil {
		t.Fatal("nil must map to nil")
	}
	orig := At(Skip, CodeIndexMismatch, 12, "shifted entry")
	got := As(fmt.Errorf("wrap: %w", orig))
	if got != orig {
		t.Fatalf("wrapped *Error not recovered: %+v", got)
	}
}

func TestIsAndAsError(t *testing.T) {
	wrapped := fmt.Errorf("outer: %w", New(Recoverable, CodeOversizedAU, "too big"))
	if !Is(wrapped, CodeOversizedAU) {
		t.Fatal("Is failed to find wrapped code")
	}
	if Is(errors.New("plain"), CodeOversizedAU) {
		t.Fatal("plain error must not match a code")
	}
	var target *Error
	if !AsError(wrapped, &target) || target.Code != CodeOversizedAU {
		t.Fatal("AsError failed")
	}
}
