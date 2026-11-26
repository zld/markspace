package index

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"assetpack/internal/adapter"
	"assetpack/internal/clock"
)

func testAsset() *adapter.Asset {
	tr := &adapter.Track{ID: "video", Kind: "video", Codec: adapter.ID{Name: adapter.CodecRGBA8, Version: 1}, Duration: 15000}
	for i := 0; i < 4; i++ {
		tr.Samples = append(tr.Samples, adapter.Sample{
			Index: i, TrackID: "video", PTS: clock.Ticks(i * 3750), Duration: 3750,
			Offset: int64(i * 16), Size: 16, Key: i%2 == 0,
			Preroll: prerollFor(i),
		})
	}
	return &adapter.Asset{Duration: 15000, SourceSHA: "abc", Tracks: []*adapter.Track{tr}}
}

func prerollFor(i int) []int {
	if i == 1 {
		return []int{0}
	}
	if i == 3 {
		return []int{2}
	}
	return nil
}

func TestBuildSaveLoadAndReadExactRange(t *testing.T) {
	idx, err := Build(testAsset(), "input.mp4")
	if err != nil {
		t.Fatal(err)
	}
	path, err := idx.Save(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	source := bytes.NewReader(make([]byte, 64))
	r := NewReader(loaded, source)
	cov, err := r.ReadRange(context.Background(), "video", clock.Range{From: 3751, To: 11251}, true)
	if err != nil {
		t.Fatal(err)
	}
	if cov.Actual != (clock.Range{From: 3750, To: 15000}) {
		t.Fatalf("actual=%+v", cov.Actual)
	}
	if len(cov.Records) != 3 || len(cov.Preroll) != 2 {
		t.Fatalf("records=%d preroll=%+v", len(cov.Records), cov.Preroll)
	}
	if cov.Preroll[0].Index != 0 || cov.Preroll[1].Index != 2 {
		t.Fatalf("preroll must be sorted: %+v", cov.Preroll)
	}
	if cov.MaxRoundedAlignSamples != 1 {
		t.Fatalf("sub-sample endpoints should round to one sample: %+v", cov)
	}
	exact, err := r.ReadRange(context.Background(), "video", clock.Range{From: 3750, To: 7500}, false)
	if err != nil {
		t.Fatal(err)
	}
	if exact.MaxRoundedAlignSamples != 0 || exact.Actual.From != 3750 || exact.Actual.To != 7500 {
		t.Fatalf("exact coverage failed: %+v", exact)
	}
}

func TestRangeBeyondDurationIsLocated(t *testing.T) {
	idx, _ := Build(testAsset(), "x")
	r := NewReader(idx, bytes.NewReader(nil))
	_, err := r.ReadRange(context.Background(), "video", clock.Range{From: 12000, To: 15001}, false)
	if err == nil {
		t.Fatal("expected duration error")
	}
	var le *clock.Error
	if !errors.As(err, &le) || le.Stage != "range" {
		t.Fatalf("expected located range error, got %v", err)
	}
}

func TestPageCacheHonorsFourMiB(t *testing.T) {
	c := NewPageCache(MaxPageCacheBytes)
	for i := 0; i < 10; i++ {
		c.Put(CachePage{Key: string(rune('a' + i)), Bytes: 1 << 20, Value: i})
		if c.Bytes() > MaxPageCacheBytes {
			t.Fatalf("cache bytes %d exceed 4MiB", c.Bytes())
		}
	}
	if _, ok := c.Get("a"); ok {
		t.Fatal("oldest page should have been evicted")
	}
	c.Put(CachePage{Key: "too-big", Bytes: MaxPageCacheBytes + 1})
	if c.Bytes() > MaxPageCacheBytes {
		t.Fatal("oversized page must not make cache exceed ceiling")
	}
	if _, ok := c.Get("too-big"); ok {
		t.Fatal("oversized page should not be cached")
	}
}
