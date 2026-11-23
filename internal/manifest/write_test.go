package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mediaforensics/internal/model"
)

func TestWriterRejectsTraversal(t *testing.T) {
	bad := []string{"", "../escape", "a/../../etc", "sub/../.."}
	for _, p := range bad {
		if _, err := NewWriter(p); err == nil {
			t.Fatalf("path %q must be rejected", p)
		}
	}
}

func TestStageAndRunManifestRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "evidence")
	w, err := NewWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec := StageRecord{
		Name:   StageParse,
		Status: "degraded",
		Offset: 2153,
		Errors: []*model.ForensicError{
			model.NewError(model.StageParseBox, "boundary", -1, 2153, "truncated mdat"),
		},
	}
	path, err := w.WriteStage(rec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stage artifact missing: %v", err)
	}
	rm := RunManifest{
		Seed: 42, Case: "normal", FrameCount: 10,
		SyncErrorMs: 1.5, RecoveredRate: 0.97, P95LatencyMs: 30,
		ErrorSummary: ErrorDigest(rec.Errors), ExitCode: 0,
	}
	manifestPath, sha, err := w.WriteRun(rm)
	if err != nil {
		t.Fatal(err)
	}
	if len(sha) != 64 || !isHex(sha) {
		t.Fatalf("manifest checksum must be 64 hex chars, got %q", sha)
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), sha) {
		t.Fatal("run manifest must embed its checksum")
	}
}

func TestManifestDeterministic(t *testing.T) {
	errs := []*model.ForensicError{
		model.NewError(model.StageRepair, "corrupt", 1, 100, "a"),
		model.NewError(model.StageValidate, "sync_threshold", 3, 200, "b"),
	}
	first := ErrorDigest(errs)
	second := ErrorDigest(errs)
	if len(first) != 2 || first[0] != second[0] || first[1] != second[1] {
		t.Fatal("error digest not stable")
	}
	body1, _ := marshalStable(RunManifest{Seed: 1, ErrorSummary: first})
	body2, _ := marshalStable(RunManifest{Seed: 1, ErrorSummary: second})
	if string(body1) != string(body2) {
		t.Fatal("manifest encoding not deterministic")
	}
}

func TestSafeJoinBlocksEscape(t *testing.T) {
	w, err := NewWriter(filepath.Join(t.TempDir(), "ev"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../x.json", "/etc/passwd", "sub/../../x"} {
		if _, err := w.safeJoin(name); err == nil {
			t.Fatalf("artifact name %q must be blocked", name)
		}
	}
	good, err := w.safeJoin("stage-parse.json")
	if err != nil || !strings.HasPrefix(good, w.Dir()) {
		t.Fatalf("valid artifact rejected: %s %v", good, err)
	}
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return len(s) == 64
}
