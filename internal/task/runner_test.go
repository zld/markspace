package task_test

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"assetcheck/internal/fixture"
	"assetcheck/internal/task"
)

func acceptanceInput(path string) task.Input {
	opts := fixture.DefaultOptions()
	return task.Input{
		SourcePath:        path,
		AssetID:           opts.AssetID,
		RequiredLanguages: fixture.RequiredLanguages(),
		ExpectedCues:      fixture.ExpectedCues(opts.Cues),
	}
}

func writeClean(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := fixture.WriteFile(path, fixture.DefaultOptions()); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCrashAfterParsedResumesWithSameDigest(t *testing.T) {
	dir := t.TempDir()
	source := writeClean(t, dir, "clean.mkv")
	in := acceptanceInput(source)

	ref, err := task.NewRunner(filepath.Join(dir, "reference")).Process(in)
	if err != nil {
		t.Fatal(err)
	}

	work := filepath.Join(dir, "work")
	crashIn := in
	crashIn.CrashAfter = task.StageParsed
	_, err = task.NewRunner(work).Process(crashIn)
	if !errors.Is(err, task.ErrInterrupted) {
		t.Fatalf("crash error = %v, want ErrInterrupted", err)
	}

	st, err := task.NewStore(work).Load(ref.Key)
	if err != nil {
		t.Fatal(err)
	}
	if st == nil || st.Stage != task.StageParsed {
		t.Fatalf("checkpoint state = %+v, want parsed", st)
	}

	resume, err := task.NewRunner(work).Process(in)
	if err != nil {
		t.Fatal(err)
	}
	if resume.ResumeFrom != task.StageParsed {
		t.Fatalf("resume from = %q, want parsed", resume.ResumeFrom)
	}
	if resume.ManifestSHA256 != ref.ManifestSHA256 {
		t.Fatalf("resume SHA %s != reference %s", resume.ManifestSHA256, ref.ManifestSHA256)
	}

	again, err := task.NewRunner(work).Process(in)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Deduplicated || again.ResumeFrom != task.StageDone {
		t.Fatalf("second delivery = %+v, want done deduplication", again)
	}
	if again.ManifestSHA256 != ref.ManifestSHA256 {
		t.Fatal("redelivered manifest digest changed")
	}
}

func TestConcurrentDuplicateDeliveriesCommitOnce(t *testing.T) {
	dir := t.TempDir()
	source := writeClean(t, dir, "concurrent.mkv")
	in := acceptanceInput(source)
	work := filepath.Join(dir, "work")

	const n = 8
	results := make([]*task.Result, n)
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = task.NewRunner(work).Process(in)
		}(i)
	}
	close(start)
	wg.Wait()

	committed := 0
	var sha string
	for i, err := range errs {
		if err != nil {
			t.Fatalf("delivery %d: %v", i, err)
		}
		if results[i] == nil {
			t.Fatalf("delivery %d returned nil result", i)
		}
		if results[i].Committed {
			committed++
		}
		if sha == "" {
			sha = results[i].ManifestSHA256
		} else if results[i].ManifestSHA256 != sha {
			t.Fatalf("delivery %d SHA %s != %s", i, results[i].ManifestSHA256, sha)
		}
	}
	if committed != 1 {
		t.Fatalf("committed count = %d, want exactly 1", committed)
	}

	sequential, err := task.NewRunner(work).Process(in)
	if err != nil {
		t.Fatal(err)
	}
	if sequential.Committed || !sequential.Deduplicated {
		t.Fatalf("sequential redelivery committed=%v dedup=%v", sequential.Committed, sequential.Deduplicated)
	}
}

func TestIdenticalReplacementRebuildsIndexButHitsContentCache(t *testing.T) {
	dir := t.TempDir()
	source := writeClean(t, dir, "replace.mkv")
	opts := fixture.DefaultOptions()
	in := acceptanceInput(source)
	work := filepath.Join(dir, "work")

	first, err := task.NewRunner(work).Process(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.ReplaceWithSameBytes(source, opts); err != nil {
		t.Fatal(err)
	}
	second, err := task.NewRunner(work).Process(in)
	if err != nil {
		t.Fatal(err)
	}
	if !second.IndexRebuilt {
		t.Fatal("expected structure index rebuild after inode/identity replacement")
	}
	if !second.CacheHit {
		t.Fatal("expected content cache hit for byte-identical replacement")
	}
	if second.ManifestSHA256 != first.ManifestSHA256 {
		t.Fatal("identical replacement changed manifest digest")
	}
}

func TestMetadataChangeInvalidatesIndexAndContentCache(t *testing.T) {
	dir := t.TempDir()
	opts := fixture.DefaultOptions()
	source := filepath.Join(dir, "metadata.mkv")
	if err := fixture.WriteFile(source, opts); err != nil {
		t.Fatal(err)
	}
	in := acceptanceInput(source)
	work := filepath.Join(dir, "work")
	if _, err := task.NewRunner(work).Process(in); err != nil {
		t.Fatal(err)
	}

	changed := opts
	changed.WritingApp = "metadata-test-v2"
	if err := fixture.WriteFile(source, changed); err != nil {
		t.Fatal(err)
	}
	second, err := task.NewRunner(work).Process(in)
	if err != nil {
		t.Fatal(err)
	}
	if !second.IndexRebuilt {
		t.Fatal("metadata change did not rebuild index")
	}
	if second.CacheHit {
		t.Fatal("metadata change incorrectly hit old content cache")
	}
	if !second.CacheInvalidated {
		t.Fatal("expected old content cache entry invalidation")
	}
}
