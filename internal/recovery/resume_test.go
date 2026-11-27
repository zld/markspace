package recovery_test

import (
	"strings"
	"testing"

	"archivecheck/internal/asset"
	"archivecheck/internal/fault"
	"archivecheck/internal/pipeline"
	"archivecheck/internal/recovery"
)

func TestResumeRewindsToSharedBoundary(t *testing.T) {
	src := asset.FromBytes("r.bin", []byte(strings.Repeat("k", 20000)))
	cfg := pipeline.Config{ChunkSize: 4096, QueueSize: 2}
	cp := recovery.New(src, cfg)
	sess, err := recovery.Open(src, cp, cfg, "scan", "emit")
	if err != nil {
		t.Fatal(err)
	}
	// The consumer ran ahead of the producer before the interruption.
	if err := sess.Commit("emit", 16384); err != nil {
		t.Fatal(err)
	}
	if err := sess.Commit("scan", 8192); err != nil {
		t.Fatal(err)
	}
	boundary, err := sess.Resume("scan", "emit")
	if err != nil {
		t.Fatal(err)
	}
	if boundary != 8192 {
		t.Fatalf("boundary = %d, want 8192", boundary)
	}
	if sess.Boundary("emit") != 8192 || sess.Boundary("scan") != 8192 {
		t.Errorf("stages not aligned: scan=%d emit=%d", sess.Boundary("scan"), sess.Boundary("emit"))
	}
	for _, st := range sess.Snapshot() {
		if st.Status != recovery.StatusRunning {
			t.Errorf("stage %s status = %s, want running", st.Name, st.Status)
		}
	}
	// Continued commits must be accepted and hashed from the rewound prefix.
	if err := sess.Commit("emit", 12288); err != nil {
		t.Fatalf("commit after rewind: %v", err)
	}
	if sess.Boundary("emit") != 12288 {
		t.Errorf("emit boundary = %d", sess.Boundary("emit"))
	}
	// The rewound digest must still verify on a fresh open.
	reopened, err := recovery.Open(src, cp, cfg, "scan", "emit")
	if err != nil {
		t.Fatalf("reopen after rewind: %v", err)
	}
	if reopened.Boundary("scan") != 8192 {
		t.Errorf("reopened scan boundary = %d", reopened.Boundary("scan"))
	}
	if _, err := sess.Resume(); !fault.HasKind(err, fault.KindInvalid) {
		t.Errorf("resume without stages accepted: %v", err)
	}
}
