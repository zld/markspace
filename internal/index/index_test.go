package index

import (
	"os"
	"testing"

	"archivist/internal/fixture"
	"archivist/internal/repair"
	"archivist/internal/scan"
)

func prepare(t *testing.T, dir string, opts fixture.Options, degrade *fixture.Degrade) (*scan.Container, *repair.Result) {
	t.Helper()
	var s *fixture.Sample
	var err error
	if degrade != nil {
		s, err = fixture.BuildDegraded(dir, opts, *degrade)
	} else {
		s, err = fixture.Build(dir, opts)
	}
	if err != nil {
		t.Fatal(err)
	}
	r, err := scan.Open(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	c, err := r.Scan()
	if err != nil {
		t.Fatal(err)
	}
	rep, err := repair.Repair(c, repair.DefaultPolicy(c.Tracks[0].Timescale))
	if err != nil {
		t.Fatal(err)
	}
	return c, rep
}

func TestBuildIndexFromRepair(t *testing.T) {
	dir := t.TempDir()
	opts := fixture.DefaultOptions("n", fixture.KindNormal)
	opts.SampleCount = 30
	c, rep := prepare(t, dir, opts, nil)
	ix, err := Build(rep)
	if err != nil {
		t.Fatal(err)
	}
	if ix.EntryCount() != 30 {
		t.Fatalf("entries = %d", ix.EntryCount())
	}
	if ix.KeyframeCount() != 2 {
		t.Fatalf("keyframes = %d", ix.KeyframeCount())
	}
	tr, ok := ix.Track(c.Tracks[0].ID)
	if !ok {
		t.Fatal("track missing")
	}
	for i := 1; i < len(tr.Entries); i++ {
		if tr.Entries[i].PTS <= tr.Entries[i-1].PTS {
			t.Fatalf("index not monotonic at %d", i)
		}
	}
}

func TestQueryReturnsNearestKeyframeAtOrBefore(t *testing.T) {
	dir := t.TempDir()
	opts := fixture.DefaultOptions("n", fixture.KindNormal)
	opts.SampleCount = 60
	opts.KeyInterval = 10
	_, rep := prepare(t, dir, opts, nil)
	ix, err := Build(rep)
	if err != nil {
		t.Fatal(err)
	}
	// Keyframes sit at PTS 0, 30000, 60000, ... (step 3000, every 10).
	cases := []struct {
		target  int64
		wantPTS int64
		wantKey int
		delta   int64
	}{
		{0, 0, 1, 0},
		{1, 0, 1, 1},
		{29999, 0, 1, 29999},
		{30000, 30000, 11, 0},
		{30001, 30000, 11, 1},
		{45000, 30000, 11, 15000},
		{150000, 150000, 51, 0},
		{177000, 150000, 51, 27000},
	}
	for _, tc := range cases {
		hit, err := ix.Query(1, tc.target)
		if err != nil {
			t.Fatal(err)
		}
		if !hit.Available {
			t.Fatalf("target %d not available", tc.target)
		}
		if hit.PTS != tc.wantPTS {
			t.Fatalf("target %d: PTS %d want %d", tc.target, hit.PTS, tc.wantPTS)
		}
		if hit.Sample != tc.wantKey {
			t.Fatalf("target %d: sample %d want %d", tc.target, hit.Sample, tc.wantKey)
		}
		if hit.Delta != tc.delta {
			t.Fatalf("target %d: delta %d want %d", tc.target, hit.Delta, tc.delta)
		}
	}
}

func TestQueryBeforeFirstKeyframe(t *testing.T) {
	dir := t.TempDir()
	opts := fixture.DefaultOptions("n", fixture.KindStartOffset)
	opts.SampleCount = 20
	opts.FirstPTS = 90000
	_, rep := prepare(t, dir, opts, nil)
	ix, err := Build(rep)
	if err != nil {
		t.Fatal(err)
	}
	hit, err := ix.Query(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if hit.Available {
		t.Fatal("query before the first keyframe must be unavailable")
	}
	if hit.PTS != 90000 || hit.Delta != -90000 {
		t.Fatalf("hit = %+v", hit)
	}
}

func TestQueryUnknownTrack(t *testing.T) {
	dir := t.TempDir()
	opts := fixture.DefaultOptions("n", fixture.KindNormal)
	opts.SampleCount = 5
	_, rep := prepare(t, dir, opts, nil)
	ix, err := Build(rep)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Query(99, 0); err == nil {
		t.Fatal("expected unknown-track error")
	}
}

func TestRebuildWritesSeekableFile(t *testing.T) {
	dir := t.TempDir()
	opts := fixture.DefaultOptions("n", fixture.KindNormal)
	opts.SampleCount = 45
	opts.KeyInterval = 15
	c, rep := prepare(t, dir, opts, nil)
	outPath := dir + "/rebuilt.mp4"
	ix, err := Rebuild(c, rep, outPath, RebuildOptions{ChunkSamples: 1})
	if err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(outPath); err != nil || st.Size() == 0 {
		t.Fatalf("output missing: %v", err)
	}
	if st, _ := os.Stat(outPath + ".tmp"); st != nil {
		t.Fatal("temporary file was not cleaned up")
	}
	// Index offsets must point at the payload bytes actually in the output.
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	tr, _ := ix.Track(1)
	for i, e := range tr.Entries {
		if e.Offset < 0 || e.Offset+int64(e.Size) > int64(len(data)) {
			t.Fatalf("entry %d offset %d outside file", i, e.Offset)
		}
	}
	// Each chunk is one sample, so the queried keyframe offset must equal the
	// index offset and the query must resolve within one tick of the target.
	hit, err := ix.Query(1, 45000)
	if err != nil {
		t.Fatal(err)
	}
	if hit.PTS != 45000 || hit.Delta != 0 {
		t.Fatalf("hit = %+v", hit)
	}
	// Query in the movie timescale must agree within one sample tick.
	movieHit, err := ix.QueryMovie(1, 500)
	if err != nil {
		t.Fatal(err)
	}
	if movieHit.PTS > 45000 || 45000-movieHit.PTS > 3000 {
		t.Fatalf("movie query = %+v", movieHit)
	}
}

func TestRebuildHandlesWrapTimeline(t *testing.T) {
	dir := t.TempDir()
	opts := fixture.DefaultOptions("w", fixture.KindWrap)
	opts.SampleCount = 60
	opts.FirstPTS = 0xFFFFD8F0
	c, rep := prepare(t, dir, opts, nil)
	outPath := dir + "/wrap.mp4"
	ix, err := Rebuild(c, rep, outPath, RebuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tr, _ := ix.Track(1)
	if tr.OutputBase != int64(0xFFFFD8F0) {
		t.Fatalf("output base = %d", tr.OutputBase)
	}
	// Querying at 2^32 + start must still resolve monotonically.
	target := int64(0xFFFFD8F0) + 30*3000
	hit, err := ix.Query(1, target)
	if err != nil {
		t.Fatal(err)
	}
	if !hit.Available || hit.PTS > target {
		t.Fatalf("hit = %+v", hit)
	}
	if target-hit.PTS > 3000*15 {
		t.Fatalf("delta too large: %d", target-hit.PTS)
	}
}

func TestRebuildIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	opts := fixture.DefaultOptions("n", fixture.KindNormal)
	opts.SampleCount = 20
	c, rep := prepare(t, dir, opts, nil)
	a := dir + "/a.mp4"
	b := dir + "/b.mp4"
	if _, err := Rebuild(c, rep, a, RebuildOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Rebuild(c, rep, b, RebuildOptions{}); err != nil {
		t.Fatal(err)
	}
	da, err := os.ReadFile(a)
	if err != nil {
		t.Fatal(err)
	}
	db, err := os.ReadFile(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(da) != string(db) {
		t.Fatal("rebuild is not byte-for-byte deterministic")
	}
}
