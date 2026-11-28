package stream

import (
	"path/filepath"
	"testing"

	"avpipe/internal/fixture"
	"avpipe/internal/model"
)

func prepare(t *testing.T, name string) (string, string) {
	t.Helper()
	root := t.TempDir()
	input := filepath.Join(root, "input")
	state := filepath.Join(root, "state")
	if err := fixture.WriteCase(input, fixture.Cases()[name]); err != nil {
		t.Fatal(err)
	}
	return input, state
}

func TestIllegalStateTransition(t *testing.T) {
	cp := NewCheckpoint("d")
	if err := cp.Move(StateCommitted); err == nil {
		t.Fatal("indexed -> committed must be rejected")
	}
	if err := cp.Move(StatePlanned); err != nil {
		t.Fatal(err)
	}
	if err := cp.Move(StateIndexed); err == nil {
		t.Fatal("planned -> indexed must be rejected")
	}
}

func TestNormalRunCommitsAndVerifies(t *testing.T) {
	input, state := prepare(t, "normal")
	res, err := Run(RunRequest{InputDir: input, StateDir: state,
		MaxKbps: 900, BudgetMS: 80, TargetPermille: 800, StopAfter: -1})
	if err != nil {
		t.Fatal(err)
	}
	if res.State != StateCommitted || !res.ManifestValid {
		t.Fatalf("state=%s valid=%v", res.State, res.ManifestValid)
	}
	v, err := Verify(input, state, 900, 80, 800)
	if err != nil || !v.Valid {
		t.Fatalf("verify err=%v problems=%v", err, v.Problems)
	}
}

func TestInterruptThenResume(t *testing.T) {
	input, state := prepare(t, "damaged")
	interrupted, err := Run(RunRequest{InputDir: input, StateDir: state,
		MaxKbps: 900, BudgetMS: 80, TargetPermille: 800, StopAfter: 2})
	if err != nil {
		t.Fatal(err)
	}
	if interrupted.State != StateStreaming || interrupted.CheckpointSample != 2 {
		t.Fatalf("want streaming at sample 2, got state=%s sample=%d",
			interrupted.State, interrupted.CheckpointSample)
	}

	resumed, err := Run(RunRequest{InputDir: input, StateDir: state,
		MaxKbps: 900, BudgetMS: 80, TargetPermille: 800, StopAfter: -1, Resume: true})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.State != StateCommitted {
		t.Fatalf("resume did not commit: %s", resumed.State)
	}
	// Resumed frames were already keyed, so no conflicting duplicate writes.
	if resumed.Duplicate != 0 {
		t.Fatalf("duplicate=%d want 0", resumed.Duplicate)
	}
}

func TestReRunIsIdempotent(t *testing.T) {
	input, state := prepare(t, "normal")
	req := RunRequest{InputDir: input, StateDir: state,
		MaxKbps: 900, BudgetMS: 80, TargetPermille: 800, StopAfter: -1}
	if _, err := Run(req); err != nil {
		t.Fatal(err)
	}
	second, err := Run(req) // fresh run over the same input
	if err != nil {
		t.Fatal(err)
	}
	if second.Duplicate != 0 || second.ManifestDiff != 0 {
		t.Fatalf("duplicate=%d manifest_diff=%d want 0/0",
			second.Duplicate, second.ManifestDiff)
	}
}

func TestDamagedDefectsAreLocatable(t *testing.T) {
	input, state := prepare(t, "damaged")
	res, err := Run(RunRequest{InputDir: input, StateDir: state,
		MaxKbps: 900, BudgetMS: 80, TargetPermille: 800, StopAfter: -1})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Locatable || len(res.Defects) < 2 {
		t.Fatalf("want >=2 locatable defects, got %+v", res.Defects)
	}
	var sawTrunc, sawIllegal bool
	for _, d := range res.Defects {
		if d.Offset < 0 || d.Track == 0 {
			t.Fatalf("defect missing location: %+v", d)
		}
		switch d.Code {
		case string(model.CodeTruncated):
			sawTrunc = true
			if !d.Recoverable {
				t.Fatal("truncation must be recoverable")
			}
		case string(model.CodeIllegalTimestamp):
			sawIllegal = true
			if d.Recoverable {
				t.Fatal("illegal boundary must be fatal to its track")
			}
		}
	}
	if !sawTrunc || !sawIllegal {
		t.Fatalf("missing trunc=%v illegal=%v", sawTrunc, sawIllegal)
	}
}
