package evidence

import (
	"bytes"
	"errors"
	"testing"

	"github.com/broadcast/mediad/internal/fixture"
	"github.com/broadcast/mediad/internal/model"
	"github.com/broadcast/mediad/internal/probe"
	"github.com/broadcast/mediad/internal/repair"
)

func TestHashStableAndMatchesOnRoundTrip(t *testing.T) {
	raw := fixture.MustGenerate(fixture.StandardSet(23)[0])
	errs := &model.ErrorSet{}
	g, _, err := probe.Probe(bytes.NewReader(raw), int64(len(raw)), probe.DefaultConfig(), errs)
	if err != nil {
		t.Fatal(err)
	}
	dmg := repair.Locate(g, errs, int64(len(raw)))
	dec, err := repair.Adjudicate(g, dmg, errs, repair.DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	h1, err := HashSegments(bytes.NewReader(raw), g, dec.Segments)
	if err != nil {
		t.Fatalf("hash 1: %v", err)
	}
	h2, err := HashSegments(bytes.NewReader(raw), g, dec.Segments)
	if err != nil {
		t.Fatalf("hash 2: %v", err)
	}
	if !StableDigests(h1, h2) {
		t.Fatal("identical input must produce identical digests")
	}
	if len(h1) == 0 {
		t.Fatal("expected segment hashes for both tracks")
	}
	for _, h := range h1 {
		if len(h.SHA256) != 64 || h.Bytes == 0 {
			t.Fatalf("malformed hash record: %+v", h)
		}
	}
}

func TestTruncatedPayloadIsReportedNotZeroFilled(t *testing.T) {
	// Use a structurally complete fixture for the graph, then back it with a
	// shortened byte slice so a late sample's offset cannot be fully read.
	raw := fixture.MustGenerate(fixture.StandardSet(23)[0])
	errs := &model.ErrorSet{}
	g, _, err := probe.Probe(bytes.NewReader(raw), int64(len(raw)), probe.DefaultConfig(), errs)
	if err != nil {
		t.Fatal(err)
	}
	dmg := repair.Locate(g, errs, int64(len(raw)))
	dec, err := repair.Adjudicate(g, dmg, errs, repair.DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	// Sanity: the full backing data hashes cleanly before truncation.
	if _, err := HashSegments(bytes.NewReader(raw), g, dec.Segments); err != nil {
		t.Fatalf("full data should hash: %v", err)
	}
	// Cut one byte into the last sample so its ReadAt cannot complete.
	cut := int(g.Tracks[g.IDs()[len(g.IDs())-1]].Samples[0].Offset) + 1
	// Force hashing of the last track's tail, whose offsets sit past the cut.
	lastID := g.IDs()[len(g.IDs())-1]
	lt := g.Tracks[lastID]
	tail := map[uint32][]*repair.Segment{
		lastID: {{
			TrackID: lastID, FirstSample: lt.SampleCount() - 1,
			LastSample: lt.SampleCount() - 1, Status: repair.StatusValid,
		}},
	}
	if _, err := HashSegments(bytes.NewReader(raw[:cut]), g, tail); err == nil {
		t.Fatal("truncated payload must surface a read error, not a fake digest")
	} else {
		var tpe *TruncatedPayloadError
		if !errors.As(err, &tpe) {
			t.Fatalf("want TruncatedPayloadError, got %T: %v", err, err)
		}
	}
}

func TestCompareRates(t *testing.T) {
	a := []SegmentHash{
		{TrackID: 1, FirstSample: 0, LastSample: 3, SHA256: "aa"},
		{TrackID: 1, FirstSample: 4, LastSample: 7, SHA256: "bb"},
	}
	b := []SegmentHash{
		{TrackID: 1, FirstSample: 4, LastSample: 7, SHA256: "bb"},
		{TrackID: 1, FirstSample: 0, LastSample: 3, SHA256: "aa"},
	}
	if got := Compare(a, b); got != 1 {
		t.Fatalf("order-independent compare = %.2f, want 1", got)
	}
	b[0].SHA256 = "cc"
	if got := Compare(a, b); got != 0.5 {
		t.Fatalf("half match = %.2f, want 0.5", got)
	}
	if Compare(nil, nil) != 1 {
		t.Fatal("empty sets must compare as full match")
	}
}
