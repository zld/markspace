package derive

import (
	"os"
	"path/filepath"
	"testing"

	"replaycheck/internal/policy"
	"replaycheck/internal/split"
	"replaycheck/internal/store"
	"replaycheck/internal/timeline"
)

func setup(t *testing.T) (*Deriver, *split.Plan, *policy.Result, string) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "source.bin")
	body := make([]byte, 4096)
	for i := range body {
		body[i] = byte(i)
	}
	if err := os.WriteFile(src, body, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	plan := &split.Plan{Segments: []split.Segment{
		{Index: 0, Span: timeline.Span{Start: 0, End: 1000}, Bytes: 2048, Ranges: []split.SampleRef{
			{TrackID: 1, Offset: 0, Size: 1024}, {TrackID: 1, Offset: 1024, Size: 1024},
		}},
		{Index: 1, Span: timeline.Span{Start: 1000, End: 2000}, Bytes: 2048, Ranges: []split.SampleRef{
			{TrackID: 1, Offset: 2048, Size: 2048},
		}},
	}}
	plan.Segments[0].StartName = "open"
	plan.Segments[1].StartName = "cut"
	res := &policy.Result{PolicyDigest: store.Key("policy"), Choices: []policy.Choice{
		{Segment: 0, Level: "compact", QualityPPM: 920_000, Bytes: 6000},
		{Segment: 1, Level: "compact", QualityPPM: 920_000, Bytes: 6000},
	}}
	d := &Deriver{
		Source: f, SourceDigest: store.Key("source"),
		Blobs:        store.NewBlobStore(filepath.Join(dir, "blobs")),
		Cache:        store.NewDerived(filepath.Join(dir, "cache")),
		PolicyDigest: res.PolicyDigest, Buffer: 128,
	}
	return d, plan, res, dir
}

func TestDeriveHitsCacheOnRerun(t *testing.T) {
	d, plan, res, dir := setup(t)
	first, err := d.Derive(plan, res)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].Cached {
		t.Fatalf("first run %+v", first)
	}
	// Artifacts are exactly the negotiated byte allowance.
	for _, c := range first {
		if c.Bytes != 6000 {
			t.Fatalf("clip %d bytes %d != 6000", c.Segment, c.Bytes)
		}
		if st := d.Cache.Stats(); st.Written != 2 {
			t.Fatalf("writes %+v", st)
		}
	}
	// Reopen the source and rerun from scratch: everything must be a hit
	// with identical digests.
	src := filepath.Join(dir, "source.bin")
	f, _ := os.Open(src)
	defer f.Close()
	d2 := &Deriver{
		Source: f, SourceDigest: d.SourceDigest,
		Blobs:        store.NewBlobStore(filepath.Join(dir, "blobs")),
		Cache:        store.NewDerived(filepath.Join(dir, "cache")),
		PolicyDigest: d.PolicyDigest, Buffer: 128,
	}
	second, err := d2.Derive(plan, res)
	if err != nil {
		t.Fatal(err)
	}
	for i := range first {
		if !second[i].Cached {
			t.Fatalf("clip %d missed cache", i)
		}
		if second[i].Digest != first[i].Digest {
			t.Fatalf("clip %d digest changed", i)
		}
	}
}

func TestDeriveInvalidatesOnSourceChange(t *testing.T) {
	d, plan, res, dir := setup(t)
	first, err := d.Derive(plan, res)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(filepath.Join(dir, "source.bin"))
	defer f.Close()
	d2 := &Deriver{
		Source: f, SourceDigest: store.Key("different-source"),
		Blobs:        store.NewBlobStore(filepath.Join(dir, "blobs")),
		Cache:        store.NewDerived(filepath.Join(dir, "cache")),
		PolicyDigest: d.PolicyDigest, Buffer: 128,
	}
	second, err := d2.Derive(plan, res)
	if err != nil {
		t.Fatal(err)
	}
	for i := range first {
		if second[i].Cached {
			t.Fatalf("clip %d wrongly hit cache for a new source", i)
		}
	}
}

func TestDeriveTruncationIsRecorded(t *testing.T) {
	d, plan, res, _ := setup(t)
	res.Choices[0].Bytes = 300
	clips, err := d.Derive(plan, res)
	if err != nil {
		t.Fatal(err)
	}
	if !clips[0].Truncated {
		t.Fatal("expected truncation flag when allowance is below source size")
	}
}
