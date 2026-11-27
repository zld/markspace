package model

import (
	"errors"
	"strings"
	"testing"
)

func TestStableErrorLayout(t *testing.T) {
	err := ErrOpusTOC.At("opus-a", 42, "ingest", 128, "bad config 31")
	got := err.Error()
	for _, want := range []string{"ERR_OPUS_TOC", "track=opus-a", "seq=42", "stage=ingest", "offset=128", "bad config 31"} {
		if !strings.Contains(got, want) {
			t.Fatalf("error %q missing %q", got, want)
		}
	}
}

func TestZeroFieldsOmitted(t *testing.T) {
	err := ErrUsage.At("", 0, "", 0, "missing --out")
	if strings.Contains(err.Error(), "track=") || strings.Contains(err.Error(), "seq=") {
		t.Fatalf("zero fields must be omitted: %q", err.Error())
	}
}

func TestExitStatus(t *testing.T) {
	if ExitStatus(nil) != 0 {
		t.Fatal("nil error -> 0")
	}
	if ExitStatus(ErrUsage.At("", 0, "", 0, "x")) != 1 {
		t.Fatal("usage error -> 1")
	}
	if ExitStatus(ErrGraphCycle.At("v1", 0, "graph", 0, "x")) != 2 {
		t.Fatal("processing error -> 2")
	}
	if ExitStatus(errors.New("plain")) != 2 {
		t.Fatal("unknown error -> 2")
	}
}

func TestAsErrorWrapsUnknown(t *testing.T) {
	me := AsError(errors.New("boom"))
	if me.Code != ErrStateCorrupt {
		t.Fatalf("got code %s", me.Code)
	}
}
