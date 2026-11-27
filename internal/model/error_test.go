package model

import (
	"errors"
	"testing"
)

func TestErrorError(t *testing.T) {
	e := NewError(CodeTruncatedPacket, StageCodec, 7, 1024, "aac frame header cut short")
	got := e.Error()
	want := "TRUNCATED_PACKET: stage=codec seq=7 offset=1024 aac frame header cut short"
	if got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestErrorIs(t *testing.T) {
	e := NewError(CodePacketLost, StageRTP, 3, 0, "")
	if !errors.Is(e, &Error{Code: CodePacketLost}) {
		t.Fatal("errors.Is should match same code")
	}
	if errors.Is(e, &Error{Code: CodeIllegalPacket}) {
		t.Fatal("errors.Is should not match different code")
	}
}
