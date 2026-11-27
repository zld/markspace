package migrate

import (
	"os"
	"path/filepath"
	"testing"

	"mediacore/internal/clock"
	"mediacore/internal/fault"
)

const sourceManifest = `{
  "version": 1,
  "entries": [
    {"id": "v0", "codec": "av1", "path": "v0.obu", "time_ms": 0, "duration_ms": 40},
    {"id": "v1", "codec": "av1", "path": "v1.obu", "time_ms": 40, "duration_ms": 40},
    {"id": "v2", "codec": "av1", "path": "v2.obu", "time_ms": 80, "duration_ms": 40},
    {"id": "v3", "codec": "av1", "path": "v3.obu", "time_ms": 120, "duration_ms": 40}
  ]
}`

func setup(t *testing.T) Options {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "manifest.v1.json"), []byte(sourceManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return DefaultOptions(dir)
}

func TestRunMigratesAndVerifies(t *testing.T) {
	opts := setup(t)
	rep, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Stage != StageDone || rep.Entries != 4 || rep.Alignment > clock.Millisecond {
		t.Fatalf("report = %+v", rep)
	}
	target, err := os.ReadFile(filepath.Join(opts.Dir, opts.TargetName))
	if err != nil || len(target) == 0 {
		t.Fatalf("target missing: %v", err)
	}
}

func TestRerunIsNoop(t *testing.T) {
	opts := setup(t)
	first, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(opts.Dir, opts.TargetName))
	second, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(opts.Dir, opts.TargetName))
	if !second.Noop || second.Entries != first.Entries || second.Digest != first.Digest {
		t.Fatalf("second = %+v", second)
	}
	if string(before) != string(after) {
		t.Fatal("re-run changed the committed manifest")
	}
}

func TestInterruptThenResume(t *testing.T) {
	for _, stage := range StageOrder() {
		if stage == StageDone {
			continue
		}
		t.Run(stage, func(t *testing.T) {
			opts := setup(t)
			opts.InterruptAt = stage
			_, err := Run(opts)
			f, ok := fault.As(err)
			if !ok || f.Code != fault.CodeMigrationInterrupted || f.ExitStatus != 3 {
				t.Fatalf("interrupt at %s: %v", stage, err)
			}
			point, err := LoadPoint(opts.Dir)
			if err != nil || point == nil || point.Stage != stage {
				t.Fatalf("point = %+v err=%v", point, err)
			}
			opts.InterruptAt = ""
			rep, err := Run(opts)
			if err != nil {
				t.Fatalf("resume: %v", err)
			}
			if !rep.Resumed || rep.Stage != StageDone || rep.Entries != 4 {
				t.Fatalf("resume = %+v", rep)
			}
		})
	}
}

func TestResumeMatchesCleanDigest(t *testing.T) {
	clean, err := Run(setup(t))
	if err != nil {
		t.Fatal(err)
	}
	opts := setup(t)
	opts.InterruptAt = StageVerify
	if _, err := Run(opts); err == nil {
		t.Fatal("expected interruption")
	}
	opts.InterruptAt = ""
	resumed, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Digest != clean.Digest || resumed.Entries != clean.Entries {
		t.Fatalf("resumed %+v vs clean %+v", resumed, clean)
	}
}

func TestRollbackRestoresTarget(t *testing.T) {
	opts := setup(t)
	old := []byte(`{"version":2,"tracks":[{"name":"video","timescale":90000}],"entries":[{"id":"old","codec":"av1","track":"video","pts":0,"duration":1}]}`)
	if err := os.WriteFile(filepath.Join(opts.Dir, opts.TargetName), old, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(opts); err != nil {
		t.Fatal(err)
	}
	if err := Rollback(opts.Dir, opts.TargetName); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(opts.Dir, opts.TargetName))
	if string(got) != string(old) {
		t.Fatalf("rollback did not restore target")
	}
}

func TestStalePointIsDiscarded(t *testing.T) {
	opts := setup(t)
	opts.InterruptAt = StageWrite
	if _, err := Run(opts); err == nil {
		t.Fatal("expected interruption")
	}
	if err := os.WriteFile(filepath.Join(opts.Dir, "manifest.v1.json"), []byte(sourceManifest+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts.InterruptAt = ""
	rep, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Resumed || rep.Stage != StageDone {
		t.Fatalf("report = %+v", rep)
	}
}

func TestMissingSourceFails(t *testing.T) {
	_, err := Run(DefaultOptions(t.TempDir()))
	f, ok := fault.As(err)
	if !ok || f.Code != fault.CodeMigrationStage {
		t.Fatalf("err = %v", err)
	}
}

func TestUnknownVersionFails(t *testing.T) {
	opts := setup(t)
	if err := os.WriteFile(filepath.Join(opts.Dir, "manifest.v1.json"), []byte(`{"version":9,"entries":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(opts); err == nil {
		t.Fatal("unknown version must fail")
	}
}
