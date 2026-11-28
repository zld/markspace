package fault

import (
	"errors"
	"testing"
)

func TestErrorCarriesContext(t *testing.T) {
	err := New(ErrCorruptBox, 128, "bad size").WithTrack(3).WithNode("scan")
	if !Is(err, ErrCorruptBox) {
		t.Fatal("Is should match code")
	}
	if Is(err, ErrEmptyRange) {
		t.Fatal("Is should not match other code")
	}
	off, ok := OffsetOf(err)
	if !ok || off != 128 {
		t.Fatalf("offset = %d,%v want 128,true", off, ok)
	}
	wrapped := errors.Join(errors.New("ctx"), err)
	if !Is(wrapped, ErrCorruptBox) {
		t.Fatal("Is should unwrap joined errors")
	}
	if got := err.Error(); got == "" {
		t.Fatal("empty message")
	}
}

func TestOffsetOfNA(t *testing.T) {
	if _, ok := OffsetOf(New(ErrInvalidArg, NA, "x")); ok {
		t.Fatal("NA offset must report not-ok")
	}
	if _, ok := OffsetOf(errors.New("plain")); ok {
		t.Fatal("plain error must report not-ok")
	}
}
