package txn

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"media.rebuild/internal/journal"
	"media.rebuild/internal/manifest"
	"media.rebuild/internal/model"
)

func open(t *testing.T, root string, crash CrashPoint) *Engine {
	t.Helper()
	e, err := OpenRoot(root, crash)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

var frameNames = []string{"frame-000.png", "frame-001.png", "frame-002.png"}

func run(t *testing.T, root string, crash CrashPoint) (*Engine, error) {
	t.Helper()
	e, err := OpenRoot(root, crash)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err := e.Begin("rebuild", "render"); err != nil {
		t.Fatal(err)
	}
	var arts []manifest.Artifact
	for _, name := range frameNames {
		a, err := e.Stage("rebuild", name, []byte("frame-body-"+name))
		if err != nil {
			return e, err
		}
		arts = append(arts, a)
	}
	m := manifest.Manifest{Task: "rebuild", Generation: 1, Artifacts: arts}
	if err := e.Seal(m); err != nil {
		return e, err
	}
	if err := e.Publish("rebuild"); err != nil {
		return e, err
	}
	return e, e.Commit(m)
}

func TestCleanRunPublishes(t *testing.T) {
	root := t.TempDir()
	e, err := run(t, root, CrashNone)
	if err != nil {
		t.Fatal(err)
	}
	if e.Crashed {
		t.Fatal("clean run crashed")
	}
	m, err := ReadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Committed || len(m.Artifacts) != 3 || m.Version != 1 {
		t.Fatalf("manifest %+v", m)
	}
	for _, a := range m.Artifacts {
		if _, err := os.Stat(filepath.Join(root, a.File)); err != nil {
			t.Fatalf("artifact %s missing: %v", a.File, err)
		}
	}
	if dirExists(filepath.Join(root, "staging", "rebuild")) {
		t.Fatal("staging left behind")
	}
}

func TestCrashAfterArtifactWritesNothingCommitted(t *testing.T) {
	root := t.TempDir()
	e, err := run(t, root, CrashAfterArtifact)
	if !errors.Is(err, ErrCrashInjected) {
		t.Fatalf("err %v", err)
	}
	if !e.Crashed {
		t.Fatal("engine did not mark crashed")
	}
	if got := PhaseOf(CrashAfterArtifact); got != "artifact_written" {
		t.Fatalf("phase %q", got)
	}
	if _, err := os.Stat(ManifestPath(root)); !os.IsNotExist(err) {
		t.Fatal("manifest committed before crash")
	}
	if dirExists(filepath.Join(root, "artifacts", "rebuild")) {
		t.Fatal("artifacts published despite crash")
	}
	if !dirExists(filepath.Join(root, "staging", "rebuild")) {
		t.Fatal("staging gone; crash point did not fire where expected")
	}
}

func TestRecoverFromCrashAfterArtifactCompletesCommit(t *testing.T) {
	root := t.TempDir()
	if _, err := run(t, root, CrashAfterArtifact); !errors.Is(err, ErrCrashInjected) {
		t.Fatalf("err %v", err)
	}
	e := open(t, root, CrashNone)
	res, err := e.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if res.Recovered != 1 || res.Published != 1 || res.Missing != 0 || res.Duplicate != 0 {
		t.Fatalf("result %+v", res)
	}
	m, err := ReadManifest(root)
	if err != nil {
		t.Fatalf("manifest not completed: %v", err)
	}
	if !m.Committed || len(m.Artifacts) != 3 {
		t.Fatalf("manifest %+v", m)
	}
	files, _ := os.ReadDir(filepath.Join(root, "artifacts", "rebuild"))
	if len(files) != 3 {
		t.Fatalf("artifact count %d", len(files))
	}
	if dirExists(filepath.Join(root, "staging", "rebuild")) {
		t.Fatal("staging kept after recovery")
	}
	// Recovery is idempotent and reports the committed task without
	// republishing or duplicating.
	res2, err := e.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if res2.Recovered != 0 || res2.Published != 1 || res2.Missing != 0 || res2.Duplicate != 0 {
		t.Fatalf("second recovery %+v", res2)
	}
	files2, _ := os.ReadDir(filepath.Join(root, "artifacts", "rebuild"))
	if len(files2) != 3 {
		t.Fatalf("artifact count changed on second recovery: %d", len(files2))
	}
}

func TestRecoverFromCrashAfterStageDone(t *testing.T) {
	root := t.TempDir()
	if _, err := run(t, root, CrashAfterStageDone); !errors.Is(err, ErrCrashInjected) {
		t.Fatalf("err %v", err)
	}
	if !dirExists(filepath.Join(root, "artifacts", "rebuild")) {
		t.Fatal("artifacts should be published at this crash point")
	}
	if _, err := os.Stat(ManifestPath(root)); !os.IsNotExist(err) {
		t.Fatal("manifest should not exist yet")
	}
	e := open(t, root, CrashNone)
	res, err := e.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if res.Recovered != 1 || res.Published != 1 || res.Missing != 0 || res.Duplicate != 0 {
		t.Fatalf("result %+v", res)
	}
	files, _ := os.ReadDir(filepath.Join(root, "artifacts", "rebuild"))
	if len(files) != 3 {
		t.Fatalf("artifact count changed: %d", len(files))
	}
}

func TestRecoverFromCrashAfterManifest(t *testing.T) {
	root := t.TempDir()
	if _, err := run(t, root, CrashAfterManifest); !errors.Is(err, ErrCrashInjected) {
		t.Fatalf("err %v", err)
	}
	e := open(t, root, CrashNone)
	states, _, err := e.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if states["rebuild"].Committed {
		t.Fatal("commit marker present despite crash point")
	}
	res, err := e.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if res.Recovered != 1 || res.Published != 1 || res.Duplicate != 0 {
		t.Fatalf("result %+v", res)
	}
	files, _ := os.ReadDir(filepath.Join(root, "artifacts", "rebuild"))
	if len(files) != 3 {
		t.Fatalf("artifacts duplicated: %d", len(files))
	}
}

func TestRecoverCleansUnregisteredStagingNextToCommittedTask(t *testing.T) {
	root := t.TempDir()
	if _, err := run(t, root, CrashNone); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(root, "staging", "rebuild")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "frame-999.png"), []byte("stray"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := open(t, root, CrashNone)
	res, err := e.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if res.Published != 1 || res.Missing != 0 || res.Duplicate != 0 {
		t.Fatalf("result %+v", res)
	}
	if dirExists(stale) {
		t.Fatal("stale staging survived")
	}
	if len(res.Cleaned) != 1 {
		t.Fatalf("cleaned %v", res.Cleaned)
	}
}

func TestRecoverDetectsMissingAndDuplicate(t *testing.T) {
	root := t.TempDir()
	if _, err := run(t, root, CrashNone); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "artifacts", "rebuild", "frame-001.png")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "artifacts", "rebuild", "frame-777.png"), []byte("stray"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := open(t, root, CrashNone)
	res, err := e.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if res.Missing != 1 || res.Duplicate != 1 {
		t.Fatalf("missing %d duplicate %d", res.Missing, res.Duplicate)
	}
}

func TestRerunKeepsArtifactSetStable(t *testing.T) {
	root := t.TempDir()
	if _, err := run(t, root, CrashNone); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadDir(filepath.Join(root, "artifacts", "rebuild"))
	if _, err := run(t, root, CrashNone); !model.IsCode(err, model.CodeTxnConflict) {
		t.Fatalf("rerun error %v", err)
	}
	after, _ := os.ReadDir(filepath.Join(root, "artifacts", "rebuild"))
	if len(before) != len(after) {
		t.Fatalf("artifact set changed: %d -> %d", len(before), len(after))
	}
	e := open(t, root, CrashNone)
	res, _ := e.Recover()
	if res.Missing != 0 || res.Duplicate != 0 {
		t.Fatalf("recover after rerun: %+v", res)
	}
}

func TestStageRejectsEscapingNames(t *testing.T) {
	e := open(t, t.TempDir(), CrashNone)
	for _, name := range []string{"../x", "a/b", "..", ""} {
		if _, err := e.Stage("t", name, []byte("x")); !model.IsCode(err, model.CodeInvalidArgument) {
			t.Errorf("name %q accepted (%v)", name, err)
		}
	}
}

func TestOpenRootRejectsDangerousPaths(t *testing.T) {
	for _, root := range []string{"", ".", "/"} {
		if _, err := OpenRoot(root, CrashNone); !model.IsCode(err, model.CodeInvalidArgument) {
			t.Errorf("root %q accepted (%v)", root, err)
		}
	}
}

func TestReadManifestMissing(t *testing.T) {
	if _, err := ReadManifest(t.TempDir()); !model.IsCode(err, model.CodeIO) {
		t.Fatalf("error %v", err)
	}
}

func TestSealAndCommitDeterministic(t *testing.T) {
	root := t.TempDir()
	if _, err := run(t, root, CrashNone); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(ManifestPath(root))
	if err != nil {
		t.Fatal(err)
	}
	root2 := t.TempDir()
	if _, err := run(t, root2, CrashNone); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(ManifestPath(root2))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("manifest bytes differ between identical runs")
	}
}

func TestUnsealedTaskRecoveryPurgesStaging(t *testing.T) {
	root := t.TempDir()
	e := open(t, root, CrashNone)
	if err := e.Begin("rebuild", "render"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Stage("rebuild", "frame-000.png", []byte("x")); err != nil {
		t.Fatal(err)
	}
	res, err := e.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if res.Recovered != 0 || res.Published != 0 {
		t.Fatalf("result %+v", res)
	}
	if len(res.Cleaned) != 1 {
		t.Fatalf("cleaned %v", res.Cleaned)
	}
	if dirExists(filepath.Join(root, "staging", "rebuild")) {
		t.Fatal("unsealed staging survived")
	}
}

// A draft that disagrees with the journal's registered artifacts must not be
// published: doing so commits a manifest naming files that were never staged.
func TestRecoverRejectsDraftArtifactMismatch(t *testing.T) {
	root := t.TempDir()
	if _, err := run(t, root, CrashAfterArtifact); !errors.Is(err, ErrCrashInjected) {
		t.Fatalf("err %v", err)
	}
	l, err := journal.Open(JournalPath(root))
	if err != nil {
		t.Fatal(err)
	}
	bogus := manifest.Manifest{Version: manifest.Version, Task: "rebuild", Committed: true,
		Artifacts: []manifest.Artifact{{Name: "ghost.png", File: "artifacts/rebuild/ghost.png", Bytes: 3, SHA256: "00"}}}
	payload, err := json.Marshal(bogus)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Append(journal.Record{Type: journal.TypeManifestDraft, Task: "rebuild", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	l.Close()

	e := open(t, root, CrashNone)
	res, err := e.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if res.Recovered != 0 || res.Published != 0 || len(res.Errors) == 0 {
		t.Fatalf("mismatched draft was not refused: %+v", res)
	}
	if _, err := ReadManifest(root); err == nil {
		t.Fatal("mismatched draft was published")
	}
}

// A committed task whose manifest file vanished must have it restored from the
// durable draft, so recovery never reports an unreadable manifest as healthy.
func TestRecoverRestoresMissingManifest(t *testing.T) {
	root := t.TempDir()
	if _, err := run(t, root, CrashNone); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ManifestPath(root)); err != nil {
		t.Fatal(err)
	}
	e := open(t, root, CrashNone)
	res, err := e.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if res.Published != 1 || res.Missing != 0 {
		t.Fatalf("result %+v", res)
	}
	m, err := ReadManifest(root)
	if err != nil {
		t.Fatalf("manifest not restored: %v", err)
	}
	if !m.Committed || len(m.Artifacts) != len(frameNames) {
		t.Fatalf("restored manifest %+v", m)
	}
}

// A crash after the publish rename but before stage_done leaves the files in
// place with no stage_done record. Recovery must complete the commit without
// duplicating or re-publishing them.
func TestRecoverCompletesPublishWithoutStageDone(t *testing.T) {
	root := t.TempDir()
	e := open(t, root, CrashNone)
	if err := e.Begin("rebuild", "render"); err != nil {
		t.Fatal(err)
	}
	var arts []manifest.Artifact
	for _, name := range frameNames {
		a, err := e.Stage("rebuild", name, []byte("frame-body-"+name))
		if err != nil {
			t.Fatal(err)
		}
		arts = append(arts, a)
	}
	if err := e.Seal(manifest.Manifest{Task: "rebuild", Generation: 1, Artifacts: arts}); err != nil {
		t.Fatal(err)
	}
	// Simulate the rename landing without the stage_done append.
	published := filepath.Join(root, "artifacts", "rebuild")
	if err := os.MkdirAll(filepath.Dir(published), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "staging", "rebuild"), published); err != nil {
		t.Fatal(err)
	}
	e.Close()

	e2 := open(t, root, CrashNone)
	res, err := e2.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if res.Recovered != 1 || res.Published != 1 || res.Duplicate != 0 || res.Missing != 0 {
		t.Fatalf("result %+v", res)
	}
	files, err := os.ReadDir(published)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(frameNames) {
		t.Fatalf("artifact count %d, want %d", len(files), len(frameNames))
	}
	if _, err := ReadManifest(root); err != nil {
		t.Fatalf("manifest not completed: %v", err)
	}
}
