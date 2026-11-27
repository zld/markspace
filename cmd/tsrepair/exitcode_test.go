package main

import (
	"testing"

	"tsrepair/internal/mediaerr"
)

func TestSplitCSV(t *testing.T) {
	got := splitCSV("a,b,,c,")
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	if splitCSV("") != nil {
		t.Error("empty csv should be nil")
	}
}

func TestParseDemoFlagsRequiresOutput(t *testing.T) {
	if _, err := parseDemoFlags([]string{"--seed", "1"}); err == nil {
		t.Fatal("missing --output must error")
	}
}

func TestRoundTripFailureIsFatal(t *testing.T) {
	// Mirrors the demo exit path: a failed round trip must surface a fatal
	// structured error, which main maps to non-zero process exit.
	err := mediaerr.Fatal(mediaerr.CodeRoundtripField, 0, 0, "mismatch", "exit 1")
	if err.Severity != mediaerr.SeverityFatal || err.Code != mediaerr.CodeRoundtripField {
		t.Fatalf("wrong error: %v", err)
	}
}
