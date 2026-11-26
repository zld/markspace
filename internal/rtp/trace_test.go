package rtp

import (
	"testing"

	"example.com/archiveverify/internal/fixture"
)

func TestFaultTraceRecovery(t *testing.T) {
	ws, err := fixture.Generate(t.TempDir(), "faults")
	if err != nil {
		t.Fatal(err)
	}
	arrivals, err := ArrivalsFromSpecs(ws.Manifest.RTPTrace)
	if err != nil {
		t.Fatal(err)
	}
	rep := RecoverTrace(arrivals, Config{Window: 16})
	t.Logf("recovered=%d losses=%d unrecovered=%v dup=%d late=%d reordered=%d seqs=%v",
		rep.Recovered, rep.Losses, rep.Unrecovered, rep.Duplicates, rep.LateDropped, rep.Reordered, rep.Sequences)
	if rep.Recovered != 3 || rep.Losses != 4 {
		t.Fatalf("got recovered=%d losses=%d unrecovered=%v", rep.Recovered, rep.Losses, rep.Unrecovered)
	}
	if rep.RecoveryPct != 75 {
		t.Fatalf("recovery pct=%v", rep.RecoveryPct)
	}
}
