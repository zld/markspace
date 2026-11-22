package pipeline

import (
	"strings"
	"testing"
)

func TestFaultMatrix(t *testing.T) {
	reports, err := FaultMatrix(17)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 5 {
		t.Fatalf("reports %d", len(reports))
	}
	want := map[string]bool{
		FaultReorderWrap: false, FaultBurstLoss: false, FaultTruncatedBox: false,
		FaultLayout: false, FaultCacheForge: false,
	}
	crossSubsystem := 0
	for _, r := range reports {
		if _, ok := want[r.Class]; !ok {
			t.Fatalf("unexpected class %q", r.Class)
		}
		want[r.Class] = true
		if !r.Recovered {
			t.Fatalf("class %s not recovered: %s", r.Class, r.Detail)
		}
		if r.DetectedBy == "" || len(r.PropagatedTo) == 0 {
			t.Fatalf("class %s lacks detection or propagation", r.Class)
		}
		if len(r.PropagatedTo) >= 2 {
			crossSubsystem++
		}
		if !strings.Contains(r.Detail, "=") {
			t.Fatalf("class %s has no quantified detail", r.Class)
		}
	}
	for class, seen := range want {
		if !seen {
			t.Fatalf("class %q missing from matrix", class)
		}
	}
	if crossSubsystem < 3 {
		t.Fatalf("only %d faults cross subsystems, want >= 3", crossSubsystem)
	}
}
