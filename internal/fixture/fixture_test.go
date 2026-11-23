package fixture_test

import (
	"testing"

	"archivectl/internal/ebml"
	"archivectl/internal/fixture"
	"archivectl/internal/recover"
)

func TestNormalFixtureParsesFully(t *testing.T) {
	set := fixture.Generate(42, 8)
	doc := ebml.ParseDocument(set.Normal)
	if len(doc.Errors) != 0 {
		t.Fatalf("normal fixture must have no parse errors, got %d", len(doc.Errors))
	}
	if len(doc.Clusters) < 2 {
		t.Fatalf("want multiple clusters, got %d", len(doc.Clusters))
	}
	tracks := map[uint32]bool{}
	for _, c := range doc.Clusters {
		for _, b := range c.Blocks {
			tracks[b.TrackNumber] = true
		}
	}
	if len(tracks) < 2 {
		t.Fatalf("want interleaved multi-track blocks, got tracks %v", tracks)
	}
}

func TestUnknownLengthFixture(t *testing.T) {
	set := fixture.Generate(42, 8)
	doc := ebml.ParseDocument(set.Unknown)
	if !doc.SegmentUnknown {
		t.Fatal("segment must be flagged unknown-length")
	}
	if len(doc.Clusters) == 0 {
		t.Fatal("unknown-length segment must still yield clusters")
	}
}

func TestTruncatedFixtureSalvagesBlocks(t *testing.T) {
	set := fixture.Generate(42, 8)
	doc := ebml.ParseDocument(set.Truncated)
	idx := recover.RebuildIndex(doc)
	if !idx.Truncated {
		t.Fatal("truncated fixture must set index.Truncated")
	}
	if idx.Count() == 0 {
		t.Fatal("complete blocks must survive truncation")
	}
	if len(idx.DamagedBytes) == 0 {
		t.Fatal("truncation offset must be recorded, not treated as a sample")
	}
	for _, e := range idx.Entries {
		if !idx.OffsetHealthy(e.Offset) {
			t.Fatalf("entry at damaged offset %d treated as valid", e.Offset)
		}
	}
}

func TestCorruptFixtureReportsError(t *testing.T) {
	set := fixture.Generate(42, 8)
	doc := ebml.ParseDocument(set.Corrupt)
	idx := recover.RebuildIndex(doc)
	if len(idx.DamagedBytes) == 0 && len(doc.Errors) == 0 {
		t.Fatal("corrupted fixture must surface a structured error or damaged offset")
	}
	if totalBlocks(doc) == 0 {
		t.Fatal("pre-corruption blocks should be salvageable")
	}
}

func TestIncompleteIndexFixture(t *testing.T) {
	set := fixture.Generate(42, 8)
	doc := ebml.ParseDocument(set.Incomplete)
	idx := recover.RebuildIndex(doc)
	if !idx.Rebuilt || idx.Count() == 0 {
		t.Fatal("incomplete index must be rebuilt from recovered blocks")
	}
}

func TestRTPCaptureRoundTrip(t *testing.T) {
	set := fixture.Generate(42, 8)
	packets, window, err := fixture.DecodeRTPCapture(set.RTP)
	if err != nil {
		t.Fatal(err)
	}
	if window != 8 || len(packets) != set.RTPPackets {
		t.Fatalf("decode mismatch window=%d packets=%d want 8/%d",
			window, len(packets), set.RTPPackets)
	}
	res, err := recover.RecoverRTP(packets, window)
	if err != nil {
		t.Fatal(err)
	}
	if res.Unrecoverable == 0 {
		t.Fatal("synthetic capture contains planned losses")
	}
	if res.RecoveryRate() < 0.90 {
		t.Fatalf("in-window recovery rate %f below 0.90", res.RecoveryRate())
	}
}

func TestFixtureDeterminism(t *testing.T) {
	a := fixture.Generate(42, 8)
	b := fixture.Generate(42, 8)
	if string(a.Normal) != string(b.Normal) || string(a.RTP) != string(b.RTP) {
		t.Fatal("same seed must produce byte-identical fixtures")
	}
}

func totalBlocks(doc *ebml.Document) int {
	n := 0
	for _, c := range doc.Clusters {
		n += len(c.Blocks)
	}
	return n
}
