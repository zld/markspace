package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pairwise/mediad/internal/checkpoint"
	"github.com/pairwise/mediad/internal/fixtures"
)

func writeFile(t *testing.T, dir, name string, spec fixtures.FileSpec) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, fixtures.Build(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func movie(t *testing.T) string {
	t.Helper()
	return writeFile(t, t.TempDir(), "m.mp4", fixtures.FileSpec{
		Timescale: 1000, Brands: []string{"isom", "qt  "}, BreakStszAt: -1, BreakSizeOf: -1,
		Tracks: []fixtures.TrackSpec{{
			ID: 1, Codec: fixtures.CodecAVC, Timescale: 90000, SampleDur: 3000,
			Samples: 300, KeyEvery: 30, SampleSize: 48, SampleSeed: 5,
		}},
	})
}

func setup(t *testing.T) (*Session, *checkpoint.Store) {
	t.Helper()
	sess, err := OpenSession(movie(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	st, err := checkpoint.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return sess, st
}

func runBatch(t *testing.T, sess *Session, st *checkpoint.Store, opts BatchOptions) *BatchResult {
	t.Helper()
	p, err := sess.Planner()
	if err != nil {
		t.Fatal(err)
	}
	res, err := RunBatch(p, sess.Requests(), st, opts)
	if err != nil && res == nil {
		t.Fatalf("batch failed: %v", err)
	}
	return res
}

func TestBatchCheckpointsEvery128Samples(t *testing.T) {
	sess, st := setup(t)
	p, err := sess.Planner()
	if err != nil {
		t.Fatal(err)
	}
	plans, err := p.Plan([]Request{{TrackID: 1, StartNS: 0, EndNS: 300 * 100_000_000, MaxSpanNS: 1_000_000_000}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) < 20 {
		t.Fatalf("need many segments, got %d", len(plans))
	}
	res, err := RunBatch(p, []Request{{TrackID: 1, StartNS: 0, EndNS: 300 * 100_000_000, MaxSpanNS: 1_000_000_000}}, st, BatchOptions{SamplesPerBlock: 24})
	if err != nil && len(res.Segments) == 0 {
		t.Fatalf("batch: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Segments) != len(plans) {
		t.Fatalf("segments=%d plans=%d", len(res.Segments), len(plans))
	}
	for b := 0; b < (len(plans)+3)/4; b++ {
		if !st.Completed(b) {
			t.Fatalf("block %d not committed", b)
		}
	}
	if !st.Completed(0) || st.NextBlock() == 0 {
		t.Fatal("checkpoint state incomplete")
	}
}

func TestBatchResumeMatchesContinuousRun(t *testing.T) {
	sess, st := setup(t)
	p, err := sess.Planner()
	if err != nil {
		t.Fatal(err)
	}
	reqs := []Request{{TrackID: 1, StartNS: 0, EndNS: 300 * 100_000_000, MaxSpanNS: 800_000_000}}
	plans, err := p.Plan(reqs)
	if err != nil {
		t.Fatal(err)
	}
	const blockSize = 8 // samples per block, so many plans group into blocks
	total := 0
	for _, p := range plans {
		total += p.SampleCount
	}
	if total < 3*blockSize {
		t.Fatalf("need >=3 blocks, have %d samples over %d plans", total, len(plans))
	}
	continuous, err := RunBatch(p, reqs, st, BatchOptions{SamplesPerBlock: blockSize})
	if err != nil {
		t.Fatal(err)
	}

	// A second, independent store simulates the interrupted-then-resumed run.
	dir := t.TempDir()
	st1, err := checkpoint.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	interrupted, err := RunBatch(p, reqs, st1, BatchOptions{SamplesPerBlock: blockSize, StopAfterBlock: 2})
	if err == nil {
		t.Fatal("expected forced interruption")
	}
	if interrupted == nil || !st1.Completed(1) {
		t.Fatal("interruption did not leave committed blocks")
	}
	if got, want := len(interrupted.Segments), 2*blockSize; got > want {
		t.Fatalf("interrupted run emitted %d segments, stop point allows %d", got, want)
	}
	st1.Close()

	st2, err := checkpoint.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	resumed, err := RunBatch(p, reqs, st2, BatchOptions{SamplesPerBlock: blockSize})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.SkippedBlocks == 0 || !resumed.Recovered {
		t.Fatal("resumed run skipped no committed blocks")
	}
	if resumed.TornTail {
		t.Fatal("clean journal reported a torn tail")
	}
	if continuous.ManifestSHA256 != resumed.ManifestSHA256 {
		t.Fatalf("manifest mismatch: continuous=%s resumed=%s", continuous.ManifestSHA256, resumed.ManifestSHA256)
	}
	if len(continuous.Segments) != len(resumed.Segments) {
		t.Fatalf("segment count mismatch: %d vs %d", len(continuous.Segments), len(resumed.Segments))
	}
	byID := map[string]string{}
	for _, s := range continuous.Segments {
		byID[s.ID] = s.SHA256
	}
	for _, s := range resumed.Segments {
		if byID[s.ID] != s.SHA256 {
			t.Fatalf("segment %s hash %s != %s after resume", s.ID, s.SHA256, byID[s.ID])
		}
	}
}

func TestBatchManifestIgnoresDuplicateContent(t *testing.T) {
	// Two identical chapter plans over the same media must produce one
	// fingerprint per distinct payload, and the manifest hash must not change
	// when the duplicated plan is added.
	sess, st := setup(t)
	planner, err := sess.Planner()
	if err != nil {
		t.Fatal(err)
	}
	base := []Request{{TrackID: 1, StartNS: 0, EndNS: 100_000_000, MaxSpanNS: 1_000_000_000}}
	once, err := RunBatch(planner, base, st, BatchOptions{SamplesPerBlock: 8})
	if err != nil {
		t.Fatal(err)
	}
	st2, err := checkpoint.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	twice, err := RunBatch(planner, append(base, base...), st2, BatchOptions{SamplesPerBlock: 8})
	if err != nil {
		t.Fatal(err)
	}
	if len(twice.Segments) <= len(once.Segments) {
		t.Fatalf("duplicate plan not materialised: %d vs %d", len(twice.Segments), len(once.Segments))
	}
	if twice.ManifestSHA256 != once.ManifestSHA256 {
		t.Fatalf("manifest changed after adding duplicate content: %s vs %s",
			twice.ManifestSHA256, once.ManifestSHA256)
	}
	var dups int
	for _, d := range twice.Decisions {
		if d.Duplicate {
			dups++
		}
	}
	if dups == 0 {
		t.Fatal("duplicate decisions not reported")
	}
}
