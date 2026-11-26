package pipeline

import (
	"testing"

	"continuity/internal/media"
)

func TestCleanRunDeterministic(t *testing.T) {
	out := t.TempDir()
	r1, err := Run(Options{Case: "clean", Out: out})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := Run(Options{Case: "clean", Out: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(r1.Render.Frames) != 8 || r1.Timeline.SyncMaxNS > 20_000_000 {
		t.Fatalf("frames=%d sync=%d", len(r1.Render.Frames), r1.Timeline.SyncMaxNS)
	}
	for i := range r1.Render.Frames {
		if r1.Render.Frames[i].SHA256 != r2.Render.Frames[i].SHA256 {
			t.Fatalf("frame %d hash differs across identical runs", i+1)
		}
	}
}

func TestPipelineCrashAndRecover(t *testing.T) {
	out := t.TempDir()
	_, err := Run(Options{Case: "recovery", Out: out, InjectCrash: true})
	if media.ExitCode(err) != 75 {
		t.Fatalf("err=%v exit=%d", err, media.ExitCode(err))
	}
	r, err := Run(Options{Case: "recovery", Out: out})
	if err != nil {
		t.Fatal(err)
	}
	if r.Journal.RecoveredArtifacts != 1 || r.Journal.Published != 8 || r.Journal.Duplicates != 0 {
		t.Fatalf("journal=%+v", r.Journal)
	}
	if r.Migration == nil || r.Migration.Checkpoints != 1 {
		t.Fatalf("migration=%+v", r.Migration)
	}
}
