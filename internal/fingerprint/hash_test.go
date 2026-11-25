package fingerprint

import (
	"testing"

	"mediascan/internal/errs"
)

func TestAddAndDeterministicOrder(t *testing.T) {
	h := NewHasher()
	data := []byte("segment-bytes-A")
	tr := TimeRange{Start: 0, End: 9000, Scale: 90000}
	ref := SourceRef{Source: "a.mp4", ByteRange: ByteRange{0, int64(len(data))}, Segment: 0}
	rec, err := h.Add(data, tr, true, ref)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Hash != Sum(data) || rec.ByteLength != int64(len(data)) || !rec.KeyframeHit {
		t.Fatalf("record fields wrong: %+v", rec)
	}
	// Two distinct contents keep distinct records and first-seen order.
	other := []byte("segment-bytes-B")
	if _, err := h.Add(other, tr, false,
		SourceRef{Source: "b.mp4", ByteRange: ByteRange{0, int64(len(other))}}); err != nil {
		t.Fatal(err)
	}
	got := h.Records()
	if len(got) != 2 || got[0].Hash != Sum(data) {
		t.Fatalf("records order/count wrong: %d", len(got))
	}
	if h.Len() != 2 || h.RefCount() != 2 {
		t.Fatalf("Len=%d RefCount=%d", h.Len(), h.RefCount())
	}
}

func TestDedupMergesOnlyOnContentHash(t *testing.T) {
	h := NewHasher()
	data := []byte("identical-payload")
	tr := TimeRange{Start: 0, End: 100, Scale: 1000}
	refs := []SourceRef{
		{Source: "rep-high/seg0.m4s", ByteRange: ByteRange{0, 17}, Segment: 0, TrackID: 1},
		{Source: "rep-low/seg0.m4s", ByteRange: ByteRange{512, 529}, Segment: 0, TrackID: 2},
		{Source: "backup/seg9.m4s", ByteRange: ByteRange{40, 57}, Segment: 9, TrackID: 1},
	}
	for _, r := range refs {
		if _, err := h.Add(data, tr, true, r); err != nil {
			t.Fatal(err)
		}
	}
	if h.Len() != 1 || h.RefCount() != 3 {
		t.Fatalf("dedup: distinct=%d refs=%d, want 1/3", h.Len(), h.RefCount())
	}
	rec := h.Records()[0]
	if len(rec.Refs) != 3 {
		t.Fatalf("merged refs = %d, want 3", len(rec.Refs))
	}
	// Every source offset is preserved after the merge.
	seen := map[string]bool{}
	for _, r := range rec.Refs {
		seen[r.Source] = true
	}
	for _, r := range refs {
		if !seen[r.Source] {
			t.Fatalf("source %s lost in dedup merge", r.Source)
		}
	}
	// Different bytes with the same time range must NOT merge.
	if _, err := h.Add([]byte("different-payload-xx"), tr, false,
		SourceRef{Source: "other/seg0.m4s"}); err != nil {
		t.Fatal(err)
	}
	if h.Len() != 2 {
		t.Fatalf("distinct content must not merge: distinct=%d", h.Len())
	}
}

func TestEmptyAndInvertedRangeRejected(t *testing.T) {
	h := NewHasher()
	_, err := h.Add(nil, TimeRange{}, false, SourceRef{Source: "x"})
	se, _ := errs.AsError(err)
	if se == nil || se.Kind != errs.KindSegment {
		t.Fatalf("want segment error for empty data, got %v", err)
	}
	_, err = h.Add([]byte("x"), TimeRange{}, false,
		SourceRef{Source: "x", ByteRange: ByteRange{Start: 10, End: 2}})
	if se, _ = errs.AsError(err); se == nil || se.Kind != errs.KindSegment {
		t.Fatalf("want segment error for inverted range, got %v", err)
	}
}

func TestManifestHashStable(t *testing.T) {
	raw := []byte("#EXTM3U\n#EXTINF:1,\na.ts\n")
	h1 := ManifestHash(raw)
	h2 := ManifestHash(append([]byte(nil), raw...))
	if h1 != h2 || len(h1) != 64 {
		t.Fatalf("manifest hash unstable or wrong length: %q", h1)
	}
}

func TestRecordsSortedRefs(t *testing.T) {
	h := NewHasher()
	data := []byte("same")
	for _, src := range []string{"z.m4s", "a.m4s", "m.m4s"} {
		if _, err := h.Add(data, TimeRange{}, true, SourceRef{Source: src}); err != nil {
			t.Fatal(err)
		}
	}
	refs := h.Records()[0].Refs
	if refs[0].Source != "a.m4s" || refs[2].Source != "z.m4s" {
		t.Fatalf("refs not sorted: %+v", refs)
	}
}
