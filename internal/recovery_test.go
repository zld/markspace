package kernel

import (
	"errors"
	"testing"
)

func TestCheckpointResumeMatchesFullRun(t *testing.T) {
	stopDir := t.TempDir()
	stopRes, err := Run(RunOptions{Fixture: "damaged", Out: stopDir, StopAfter: StageCut})
	var stop *CheckpointStop
	if !errors.As(err, &stop) || ExitCodeOf(err) != 2 {
		t.Fatalf("expected exit-2 checkpoint stop, got res=%#v err=%v", stopRes, err)
	}
	if stop.Stage != StageCut || stop.Packet == 0 {
		t.Fatalf("bad stop: %#v", stop)
	}
	cp, path, err := LoadCheckpoint(stopDir)
	if err != nil {
		t.Fatal(err)
	}
	if path == "" || cp.InputPacket != stop.Packet || len(cp.TrackTimes) == 0 {
		t.Fatalf("checkpoint missing packet/track state: %#v", cp)
	}
	if cp.GapEventCount != 2 || len(cp.Gaps) != 2 {
		t.Fatalf("gap count=%d events=%d want 2", cp.GapEventCount, len(cp.Gaps))
	}

	resumeRes, err := Run(RunOptions{Fixture: "damaged", Out: stopDir, Resume: true})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumeRes.ResumeStage != StageManifest || resumeRes.ResumePacket != cp.InputPacket {
		t.Fatalf("bad resume position: %#v cp=%d", resumeRes, cp.InputPacket)
	}
	if !resumeRes.Validation.Valid || resumeRes.Cache.Created != 2 {
		t.Fatalf("resume validation=%#v cache=%#v", resumeRes.Validation, resumeRes.Cache)
	}

	again, err := Run(RunOptions{Fixture: "damaged", Out: stopDir, Resume: true})
	if err != nil {
		t.Fatal(err)
	}
	if again.Cache.Reused != 2 || again.Cache.Created != 0 {
		t.Fatalf("second resume cache=%#v want reused=2", again.Cache)
	}

	fullDir := t.TempDir()
	full, err := Run(RunOptions{Fixture: "damaged", Out: fullDir})
	if err != nil {
		t.Fatal(err)
	}
	if !full.Validation.Valid {
		t.Fatalf("full validation failed: %#v", full.Validation)
	}
	if len(resumeRes.Manifest.Segments) != 2 || len(full.Manifest.Segments) != 2 {
		t.Fatal("expected two segments")
	}
	for i := range full.Manifest.Segments {
		a := resumeRes.Manifest.Segments[i]
		b := full.Manifest.Segments[i]
		if a.ContentHash != b.ContentHash || a.SHA256 != b.SHA256 || a.Time != b.Time {
			t.Fatalf("segment %d mismatch resume=%#v full=%#v", i, a, b)
		}
	}
}

func TestSourceMutationInvalidatesOnlyNamespacedEntries(t *testing.T) {
	dir := t.TempDir()
	if _, err := Run(RunOptions{Fixture: "clean", Out: dir}); err != nil {
		t.Fatal(err)
	}
	cache, _, err := LoadCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	cache.Entries["other-source/segment-001"] = CacheEntry{SourceDigest: "other-source", SegmentID: "segment-001"}
	if _, err := cache.Save(dir); err != nil {
		t.Fatal(err)
	}

	res, err := Run(RunOptions{Fixture: "clean", Out: dir, MutateSource: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Cache.Invalidated != 2 || res.Cache.Created != 2 {
		t.Fatalf("mutation cache=%#v", res.Cache)
	}
	cache, _, _ = LoadCache(dir)
	if _, ok := cache.Entries["other-source/segment-001"]; !ok {
		t.Fatal("unrelated source cache entry was invalidated")
	}
}
