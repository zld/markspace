package job

import (
	"errors"
	"fmt"
	"testing"
)

var phases = []Phase{PhaseParse, PhaseMap, PhaseIndex}

func fns(crashAt Phase) map[Phase]PhaseFunc {
	out := map[Phase]PhaseFunc{}
	for i, ph := range phases {
		ph, i := ph, i
		out[ph] = func(prev []byte) ([]byte, error) {
			if ph == crashAt {
				return nil, errors.New("simulated crash")
			}
			// Deterministic output derived from phase name and input.
			return []byte(fmt.Sprintf("%s:%d:%s", ph, i, prev)), nil
		}
	}
	return out
}

func TestRunCompletesAndIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	r1, err := Run(store, "job1", phases, fns(""))
	if err != nil {
		t.Fatal(err)
	}
	if r1.ResumedFromCheckpoint {
		t.Fatal("fresh run must not report resume")
	}
	// Second run: everything resumes from checkpoints, digest identical.
	r2, err := Run(store, "job1", phases, fns(""))
	if err != nil {
		t.Fatal(err)
	}
	if !r2.ResumedFromCheckpoint {
		t.Fatal("second run must resume from checkpoints")
	}
	if r1.Digest != r2.Digest {
		t.Fatalf("digest changed across rerun: %s != %s", r1.Digest, r2.Digest)
	}
}

func TestCrashAndResume(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Crash during the last phase.
	if _, err := Run(store, "job1", phases, fns(PhaseIndex)); err == nil {
		t.Fatal("expected crash")
	}
	// Failure reason must be persisted.
	st, err := store.LoadState("job1")
	if err != nil {
		t.Fatal(err)
	}
	if st.Failure == "" {
		t.Fatal("failure reason not persisted")
	}
	if !st.Has(PhaseParse) || !st.Has(PhaseMap) || st.Has(PhaseIndex) {
		t.Fatalf("unexpected completion state: %+v", st.Completed)
	}
	// Resume: first two phases restore from checkpoint, index reruns.
	res, err := Run(store, "job1", phases, fns(""))
	if err != nil {
		t.Fatal(err)
	}
	if !res.ResumedFromCheckpoint {
		t.Fatal("must resume from last complete checkpoint")
	}
	st2, _ := store.LoadState("job1")
	if !st2.Done || st2.Failure != "" {
		t.Fatalf("job not cleanly done: %+v", st2)
	}
}

func TestCorruptCheckpointDetected(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(store, "job1", phases, fns("")); err != nil {
		t.Fatal(err)
	}
	// Tamper with a checkpoint while state still claims completion.
	if err := store.WriteCheckpoint(PhaseMap, []byte("tampered")); err != nil {
		t.Fatal(err)
	}
	_, err = Run(store, "job1", phases, fns(""))
	if err == nil || !contains(err.Error(), "hash mismatch") {
		t.Fatalf("want hash mismatch error, got %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
