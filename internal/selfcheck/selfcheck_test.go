package selfcheck

import (
	"io"
	"testing"
)

func TestRunAcceptanceAssertions(t *testing.T) {
	if err := Run(io.Discard); err != nil {
		t.Fatalf("self-check acceptance failed: %v", err)
	}
}
