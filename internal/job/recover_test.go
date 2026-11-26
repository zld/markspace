package job

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/manifest"
	"mediad/internal/media"
)

func buildTestTimeline() *media.Timeline {
	tb, _ := media.NewTimeBase(1000)
	tracks := []*media.Track{
		{Key: "audio", TimeBase: tb},
		{Key: "video", TimeBase: tb},
	}
	for _, tr := range tracks {
		for i := 0; i < 4; i++ {
			tr.Samples = append(tr.Samples, media.Sample{
				Index: i + 1, StartNs: int64(i) * media.NanoPerSecond,
				DurationNs: media.NanoPerSecond, DataOffset: int64(i * 16),
				Size: 16, SourceTick: int64(i),
				Payload: []byte(tr.Key + "-sample-payload-" + string(rune('0'+i))),
			})
		}
	}
	return media.NewTimeline(tracks)
}

func payloadsOf(tl *media.Timeline) map[string][][]byte {
	out := map[string][][]byte{}
	for _, tr := range tl.Tracks {
		for _, s := range tr.Samples {
			out[tr.Key] = append(out[tr.Key], s.Payload)
		}
	}
	return out
}

func testPolicy() Policy {
	return Policy{
		MaxBitrate: 1_500_000, BudgetUnits: 900,
		MinQuality: [2]int64{20, 100},
		Candidates: []PolicyCandidate{{Width: 960, Height: 540, Bitrate: 1_500_000,
			QualityN: 25, QualityD: 100, CostPerNs: 1}},
	}
}

func newHarness(t *testing.T) (*Store, *Archive, string, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	archive, err := OpenArchive(filepath.Join(dir, "archive"))
	if err != nil {
		t.Fatal(err)
	}
	tl := buildTestTimeline()
	src := SourceDigest(payloadsOf(tl))
	pol, err := PolicyDigest(testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	return store, archive, src, pol
}

func runOnce(t *testing.T, store *Store, archive *Archive, tl *media.Timeline,
	src, pol string, pause int64) *RunResult {
	t.Helper()
	r, err := NewRunner(Config{
		Store: store, Archive: archive, Timeline: tl,
		SourceDigest: src, PolicyDigest: pol, PauseAfterSample: pause,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run()
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	return res
}

func TestRunnerFullRunCompletes(t *testing.T) {
	store, archive, src, pol := newHarness(t)
	tl := buildTestTimeline()
	res := runOnce(t, store, archive, tl, src, pol, 0)
	if res.TotalSamples != 8 || res.NewWrites != 8 || res.DuplicateWrites != 0 {
		t.Fatalf("unexpected counts: %+v", res)
	}
	task, err := store.GetTask(res.TaskKey)
	if err != nil {
		t.Fatal(err)
	}
	if task.Phase != PhaseComplete {
		t.Fatalf("phase = %s, want complete", task.Phase)
	}
	if len(task.ManifestDigest) != 64 {
		t.Fatalf("manifest digest not a SHA-256 hex: %q", task.ManifestDigest)
	}
}

func TestRunnerPauseAndResume(t *testing.T) {
	store, archive, src, pol := newHarness(t)
	tl := buildTestTimeline()

	first := runOnce(t, store, archive, tl, src, pol, 4)
	if !first.Paused || first.NewWrites != 4 {
		t.Fatalf("pause did not stop after 4 samples: %+v", first)
	}
	task, _ := store.GetTask(first.TaskKey)
	if task.Phase != PhaseArchive {
		t.Fatalf("paused phase = %s, want archive", task.Phase)
	}

	second := runOnce(t, store, archive, tl, src, pol, 0)
	if second.ResumedFromSample != 4 {
		t.Fatalf("resumedFromSample = %d, want 4", second.ResumedFromSample)
	}
	if second.NewWrites != 4 || second.DuplicateWrites != 4 {
		t.Fatalf("resume writes = new %d dup %d, want 4/4",
			second.NewWrites, second.DuplicateWrites)
	}

	// A single uninterrupted run must produce the identical manifest digest.
	store2, archive2, src2, pol2 := newHarness(t)
	full := runOnce(t, store2, archive2, buildTestTimeline(), src2, pol2, 0)
	if full.ManifestDigest != second.ManifestDigest {
		t.Fatal("paused+resumed digest differs from full-run digest")
	}
}

func TestDuplicateSubmissionDedups(t *testing.T) {
	store, archive, src, pol := newHarness(t)
	tl := buildTestTimeline()
	first := runOnce(t, store, archive, tl, src, pol, 0)

	again := runOnce(t, store, archive, tl, src, pol, 0)
	if again.DuplicateWrites != 8 || again.NewWrites != 0 {
		t.Fatalf("re-run must hit dedup: %+v", again)
	}
	if again.ManifestDigest != first.ManifestDigest {
		t.Fatal("duplicate run changed the manifest digest")
	}
}

func TestIllegalTransitionRejected(t *testing.T) {
	store, _, _, _ := newHarness(t)
	if err := store.Advance("missing", PhaseQueued, PhaseComplete); err == nil {
		t.Fatal("skipping phases must be rejected")
	}
	if CanTransition(PhaseScan, PhaseComplete) {
		t.Fatal("non-adjacent transition reported as legal")
	}
}

func TestRecoverDetectsAndRepairsCorruption(t *testing.T) {
	store, archive, src, pol := newHarness(t)
	tl := buildTestTimeline()
	res := runOnce(t, store, archive, tl, src, pol, 0)

	// Corrupt the on-disk bytes of the first archived object.
	objs, err := store.Objects(res.TaskKey)
	if err != nil || len(objs) != 8 {
		t.Fatalf("objects = %d err=%v", len(objs), err)
	}
	target := objs[0]
	path, err := archive.objectPath(target.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupted-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := Recover(store, archive, res.TaskKey)
	if rep.Published {
		t.Fatal("manifest must not publish over a corrupt archive")
	}
	var me *media.Error
	if !errors.As(err, &me) || me.Stage != media.StageRecover {
		t.Fatalf("want recover-stage error, got %v", err)
	}
	if media.ClassifyExit(err) != media.ExitArchiveFailure {
		t.Fatal("corrupt archive must map to exit 3")
	}

	// Repair with the known-good payload, then re-run recovery successfully.
	good := tl.Tracks[0].Samples[target.SampleIdx-1].Payload
	if err := RearchiveObject(archive, target.Hash, good); err != nil {
		t.Fatalf("repair failed: %v", err)
	}
	rep2, err := Recover(store, archive, res.TaskKey)
	if err != nil || !rep2.Published {
		t.Fatalf("recovered run should publish: %v", err)
	}
	if rep2.ManifestDigest != res.ManifestDigest {
		t.Fatal("digest changed after repairing the same content")
	}
}

func TestSourceDigestStableAndPolicySensitive(t *testing.T) {
	tl := buildTestTimeline()
	d1 := SourceDigest(payloadsOf(tl))
	d2 := SourceDigest(payloadsOf(buildTestTimeline()))
	if d1 != d2 {
		t.Fatal("identical content must hash identically")
	}
	p1, _ := PolicyDigest(testPolicy())
	changed := testPolicy()
	changed.MaxBitrate = 999
	p2, _ := PolicyDigest(changed)
	if p1 == p2 {
		t.Fatal("policy change must produce a new policy digest")
	}
	if TaskKey(d1, p1) == TaskKey(d1, p2) {
		t.Fatal("policy change must produce a new task key")
	}
}

func TestManifestEntriesCanonical(t *testing.T) {
	store, archive, src, pol := newHarness(t)
	res := runOnce(t, store, archive, buildTestTimeline(), src, pol, 0)
	if len(res.Entries) != 8 {
		t.Fatalf("entries = %d", len(res.Entries))
	}
	// Audio sorts before video; within a track samples are ascending.
	if res.Entries[0].TrackKey != "audio" || res.Entries[0].SampleIdx != 1 {
		t.Fatalf("first entry not canonical: %+v", res.Entries[0])
	}
	if res.Entries[7].TrackKey != "video" || res.Entries[7].SampleIdx != 4 {
		t.Fatalf("last entry not canonical: %+v", res.Entries[7])
	}
	_ = manifest.Digest(res.Entries)
}
