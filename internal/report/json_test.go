package report

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteLineStable(t *testing.T) {
	line := Line{
		Transport:         "UDP",
		FinalState:        "Closed",
		StateTransitions:  5,
		PacketsReceived:   7,
		RTPSeqGaps:        1,
		ReassembledFrames: 2,
		DiscardedFrames:   1,
		DuplicatePackets:  1,
		CompleteFrames:    2,
	}

	var a, b bytes.Buffer
	if err := WriteLine(&a, line); err != nil {
		t.Fatal(err)
	}
	if err := WriteLine(&b, line); err != nil {
		t.Fatal(err)
	}
	if a.String() != b.String() {
		t.Fatalf("output not stable:\n%s\n%s", a.String(), b.String())
	}
	out := a.String()
	if !strings.HasSuffix(out, "\n") || strings.Count(out, "\n") != 1 {
		t.Fatalf("expected exactly one JSON line, got %q", out)
	}
	// fixed key order: transport must be the first key
	if !strings.HasPrefix(out, `{"transport":"UDP"`) {
		t.Fatalf("unexpected key order: %s", out)
	}
}
