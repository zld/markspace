package pipeline

import (
	"context"
	"path/filepath"
	"testing"

	"archivist/internal/fixture"
	"archivist/internal/index"
)

// TestTwoTracksShareOneMovieTimeline verifies that tracks with different
// timescales are repaired independently but expressed on the same movie
// timeline, and that the rebuilt file keeps both tracks queryable.
func TestTwoTracksShareOneMovieTimeline(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "inputs")
	opts := fixture.DefaultOptions("av", fixture.KindNormal)
	opts.SampleCount = 40
	opts.WithAudio = true
	opts.FirstPTS = 9000
	s, err := fixture.Build(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	r := newRunner(t, root, 1, 0)
	results, err := r.Run(context.Background(), []string{s.Path})
	if err != nil {
		t.Fatal(err)
	}
	res := results[0]
	if res.Status != "ok" {
		t.Fatalf("status = %s (%s)", res.Status, res.Error)
	}
	ix, err := index.Load(res.IndexPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(ix.Tracks) != 2 {
		t.Fatalf("indexed tracks = %d", len(ix.Tracks))
	}
	video, ok := ix.Track(1)
	if !ok {
		t.Fatal("video track missing")
	}
	audio, ok := ix.Track(2)
	if !ok {
		t.Fatal("audio track missing")
	}
	if video.Timescale != 90000 || audio.Timescale != 48000 {
		t.Fatalf("timescales = %d/%d", video.Timescale, audio.Timescale)
	}
	// The same wall-clock instant must map to each track's own ticks and the
	// same movie time.
	movieTick := int64(600) // 0.6s into the movie, past the 0.1s start offset
	vHit, err := ix.QueryMovie(1, movieTick)
	if err != nil {
		t.Fatal(err)
	}
	aHit, err := ix.QueryMovie(2, movieTick)
	if err != nil {
		t.Fatal(err)
	}
	if !vHit.Available || !aHit.Available {
		t.Fatalf("hits unavailable: %+v %+v", vHit, aHit)
	}
	if vHit.MovieTime != aHit.MovieTime {
		t.Fatalf("movie times disagree: %d vs %d", vHit.MovieTime, aHit.MovieTime)
	}
	// Each track's own PTS is in its own timescale, roughly 0.5s past the
	// 0.1s start offset: 0.6s of media.
	if vHit.PTS < 50000 || vHit.PTS > 56000 {
		t.Fatalf("video PTS = %d, expected ~54000", vHit.PTS)
	}
	if aHit.PTS < 27000 || aHit.PTS > 30000 {
		t.Fatalf("audio PTS = %d, expected ~28800", aHit.PTS)
	}
	if res.IndexEntries != 80 {
		t.Fatalf("index entries = %d", res.IndexEntries)
	}
}
