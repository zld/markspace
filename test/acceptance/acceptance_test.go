// Package accept holds end-to-end acceptance tests that mirror the
// commands in the project brief: the same deterministic seed drives the
// full pipeline, and every injected behavior is checked against the
// promised tolerances.
package accept

import (
	"bytes"
	"testing"

	"mediacheck/internal/demo"
	"mediacheck/internal/fixture"
	"mediacheck/internal/pipeline"
	"mediacheck/internal/stream"
)

func run(t *testing.T, name string) demo.ScenarioResult {
	t.Helper()
	results, err := demo.NewRunner(7).RunAll()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("scenario %s not found", name)
	return demo.ScenarioResult{}
}

func TestNormalHasZeroLossAndDamage(t *testing.T) {
	r := run(t, fixture.ScenarioNormal)
	if r.Report.ReorderStats.MissingPackets != 0 {
		t.Fatalf("normal stream missing packets: %d", r.Report.ReorderStats.MissingPackets)
	}
	if len(r.Report.Faults) != 0 {
		t.Fatalf("normal stream faults: %+v", r.Report.Faults)
	}
	if len(r.Report.Samples) == 0 {
		t.Fatal("normal stream produced no samples")
	}
}

func TestReorderRecoversIdenticalMapping(t *testing.T) {
	clean := fixture.NewGenerator(7).Normal(stream.CodecH264, 16, 4)
	cleanRep := pipeline.Process(stream.CodecH264, clean.SSRC, clean.ClockRate, 16,
		toDG(clean))

	shuffled := fixture.NewGenerator(7).Normal(stream.CodecH264, 16, 4)
	shuffled.ReorderAndLoss(3) // swaps only
	shufRep := pipeline.Process(stream.CodecH264, shuffled.SSRC, shuffled.ClockRate, 16,
		toDG(shuffled))

	if len(cleanRep.Samples) != len(shufRep.Samples) {
		t.Fatalf("sample count differs after reorder: %d vs %d",
			len(cleanRep.Samples), len(shufRep.Samples))
	}
	for i := range cleanRep.Samples {
		a, b := cleanRep.Samples[i], shufRep.Samples[i]
		if a.FirstSeq != b.FirstSeq || a.LastSeq != b.LastSeq ||
			a.ByteOffset != b.ByteOffset || a.ByteLength != b.ByteLength ||
			a.TimeStart != b.TimeStart || a.KeyFrame != b.KeyFrame {
			t.Fatalf("sample %d mapping differs:\n%+v\n%+v", i, a, b)
		}
	}
}

func TestDamageLocationWithinTolerances(t *testing.T) {
	r := run(t, fixture.ScenarioTruncatedNAL)

	var headSeen, nalSeen bool
	for _, f := range r.Report.Faults {
		switch string(f.Class) {
		case "truncated_header":
			headSeen = true
			// Injected at seq 9; tolerance is one packet.
			if diff(f.Seq.First, 9) > 1 {
				t.Fatalf("header fault seq %d outside 1-packet tolerance", f.Seq.First)
			}
		case "truncated_nal":
			nalSeen = true
			// Injected final FU at seq 12; reported span must end within
			// one packet of it.
			if diff(f.Seq.Last, 12) > 1 {
				t.Fatalf("nal fault last seq %d vs injected 12", f.Seq.Last)
			}
			// The fragment's first byte is offset 30; the fault must begin
			// within four bytes and one clock tick of the real region.
			if abs64(f.Byte.Offset-30) > 4 {
				t.Fatalf("nal byte offset %d outside 4-byte tolerance", f.Byte.Offset)
			}
			if abs64(f.Time.Start-36000) > 1 {
				t.Fatalf("nal time start %d outside 1-tick tolerance", f.Time.Start)
			}
		}
	}
	if !headSeen || !nalSeen {
		t.Fatalf("missing expected faults: %+v", r.Report.Faults)
	}
	// Damaged input still yields a complete report with surviving samples.
	if len(r.Report.Samples) == 0 {
		t.Fatal("damaged stream produced no samples")
	}
}

func TestReplacementInvalidatesOnlyContentCache(t *testing.T) {
	r := run(t, fixture.ScenarioReplace)
	if r.CacheStats.CacheInvalidations != 1 {
		t.Fatalf("content invalidations = %d, want 1", r.CacheStats.CacheInvalidations)
	}
	// Container/index work did not evict content and rebuilt mappings.
	if r.CacheStats.IndexBuilds < 2 {
		t.Fatalf("expected index rebuild after replacement, got %d", r.CacheStats.IndexBuilds)
	}
}

func TestMetadataUpdateKeepsContentRebuildsIndex(t *testing.T) {
	r := run(t, fixture.ScenarioMetadata)
	if r.CacheStats.CacheInvalidations != 0 {
		t.Fatalf("metadata update invalidated content: %d", r.CacheStats.CacheInvalidations)
	}
	if r.CacheStats.CacheHits < 1 {
		t.Fatal("metadata update must still hit the content cache")
	}
	if r.CacheStats.IndexBuilds < 2 {
		t.Fatalf("metadata update must rebuild the index: builds=%d", r.CacheStats.IndexBuilds)
	}
}

func TestCommitInterruptRollsBack(t *testing.T) {
	r := run(t, fixture.ScenarioCommitAbort)
	if !r.MigrateRolledBack {
		t.Fatal("commit interruption must report a rollback")
	}
	if r.Manifest == nil {
		t.Fatal("rolled-back manifest missing")
	}
	// The scenario facts already assert digest_restored and the
	// pre/post version equality; verify a fact is present.
	found := false
	for _, fact := range r.Facts {
		if fact == "digest_restored=true" {
			found = true
		}
	}
	if !found {
		t.Fatalf("rollback digest fact missing: %v", r.Facts)
	}
}

func TestSameSeedProducesStableSummary(t *testing.T) {
	first, err := demo.NewRunner(7).RunAll()
	if err != nil {
		t.Fatal(err)
	}
	second, err := demo.NewRunner(7).RunAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(second) {
		t.Fatal("scenario count differs across runs")
	}
	for i := range first {
		if first[i].Digest != second[i].Digest {
			t.Fatalf("scenario %s digest unstable:\n%s\n%s",
				first[i].Name, first[i].Digest, second[i].Digest)
		}
		if !equalFacts(first[i].Facts, second[i].Facts) {
			t.Fatalf("scenario %s facts differ:\n%v\n%v",
				first[i].Name, first[i].Facts, second[i].Facts)
		}
	}
}

func TestDifferentSeedsDiffer(t *testing.T) {
	a, _ := demo.NewRunner(7).RunAll()
	b, _ := demo.NewRunner(8).RunAll()
	same := 0
	for i := range a {
		if bytes.Equal([]byte(a[i].Digest), []byte(b[i].Digest)) {
			same++
		}
	}
	if same == len(a) {
		t.Fatal("different seeds produced identical digests")
	}
}

func toDG(s *fixture.Stream) []pipeline.Datagram {
	out := make([]pipeline.Datagram, len(s.Datagrams))
	for i, d := range s.Datagrams {
		out[i] = pipeline.Datagram{Raw: d.Raw, IsRTCP: d.IsRTCP}
	}
	return out
}

func diff(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return b - a
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func equalFacts(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
