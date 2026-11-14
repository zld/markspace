package queue

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTransitions(t *testing.T) {
	legal := [][2]State{
		{Pending, Running}, {Pending, Canceled},
		{Running, Success}, {Running, Failed}, {Running, Canceled},
		{Failed, Running}, // retry
	}
	for _, tr := range legal {
		if !tr[0].CanTransition(tr[1]) {
			t.Errorf("expected %s -> %s to be legal", tr[0], tr[1])
		}
	}
	illegal := [][2]State{
		{Pending, Success}, {Pending, Failed},
		{Success, Running}, {Success, Failed},
		{Canceled, Running}, {Failed, Success}, {Running, Pending},
	}
	for _, tr := range illegal {
		if tr[0].CanTransition(tr[1]) {
			t.Errorf("expected %s -> %s to be illegal", tr[0], tr[1])
		}
	}
}

func TestJobTransitionValidates(t *testing.T) {
	j := &Job{ID: "x", State: Pending}
	if err := j.Transition(Success); err == nil {
		t.Fatal("pending -> success must fail")
	}
	if err := j.Transition(Running); err != nil {
		t.Fatalf("pending -> running: %v", err)
	}
	if err := j.Transition(Success); err != nil {
		t.Fatalf("running -> success: %v", err)
	}
	if err := j.Transition(Running); err == nil {
		t.Fatal("success -> running must fail")
	}
}

func TestStateLogRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.jsonl")
	log, err := NewStateLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.WriteHeader("digest123"); err != nil {
		t.Fatal(err)
	}
	log.LogTransition("a", Pending, Running, 1, nil)
	log.LogTransition("a", Running, Success, 1, nil)
	log.LogTransition("b", Pending, Running, 1, nil)
	log.LogTransition("b", Running, Failed, 1, os.ErrInvalid)
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	digest, final, err := ReadLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if digest != "digest123" {
		t.Fatalf("digest = %q", digest)
	}
	if final["a"] != Success || final["b"] != Failed {
		t.Fatalf("final states = %v", final)
	}
}
