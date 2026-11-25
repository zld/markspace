package pipeline

import "testing"

func TestStageKeyStableAndDistinct(t *testing.T) {
	s1 := Stage{Name: "fingerprint", InputDigest: "abc", Params: "chunk=8"}
	s2 := Stage{Name: "fingerprint", InputDigest: "abc", Params: "chunk=8"}
	s3 := Stage{Name: "fingerprint", InputDigest: "abd", Params: "chunk=8"}
	if s1.Key() != s2.Key() {
		t.Fatal("identical stage invocation must yield a stable key")
	}
	if s1.Key() == s3.Key() {
		t.Fatal("different input digest must change the key")
	}
	if len(s1.Key()) != 64 {
		t.Fatalf("stage key length = %d, want 64 hex chars", len(s1.Key()))
	}
}

func TestStateIdempotent(t *testing.T) {
	st := NewState()
	key := Stage{Name: "archive", InputDigest: "x", Params: "p"}.Key()
	st.MarkCompleted(StageResult{Stage: "archive", Key: key, OutputDigest: "out"})
	if !st.IsDone(key) {
		t.Fatal("marked stage must report done")
	}
	// Re-marking the same key must not duplicate the result.
	st.MarkCompleted(StageResult{Stage: "archive", Key: key, OutputDigest: "out"})
	if len(st.Results()) != 1 {
		t.Fatalf("results = %d, want 1", len(st.Results()))
	}
}

func TestStageOrder(t *testing.T) {
	want := []string{"ingest", "normalize", "fingerprint", "archive"}
	if len(StageOrder) != len(want) {
		t.Fatalf("stage order = %v", StageOrder)
	}
	for i := range want {
		if StageOrder[i] != want[i] {
			t.Fatalf("stage %d = %s, want %s", i, StageOrder[i], want[i])
		}
	}
}
