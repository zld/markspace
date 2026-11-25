package pipeline

import (
	"testing"

	"mediacheck/internal/cache"
	"mediacheck/internal/fault"
	"mediacheck/internal/fixture"
	"mediacheck/internal/stream"
)

func toDatagrams(s *fixture.Stream) []Datagram {
	out := make([]Datagram, len(s.Datagrams))
	for i, d := range s.Datagrams {
		out[i] = Datagram{Raw: d.Raw, IsRTCP: d.IsRTCP}
	}
	return out
}

func TestNormalStreamZeroLossZeroDamage(t *testing.T) {
	s := fixture.NewGenerator(7).Normal(stream.CodecH264, 12, 4)
	rep := Process(stream.CodecH264, s.SSRC, s.ClockRate, 16, toDatagrams(s))

	if rep.ReorderStats.MissingPackets != 0 {
		t.Fatalf("normal stream has missing packets: %+v", rep.ReorderStats)
	}
	if len(rep.Faults) != 0 {
		t.Fatalf("normal stream has faults: %+v", rep.Faults)
	}
	if len(rep.Samples) != 12 {
		t.Fatalf("samples = %d, want 12", len(rep.Samples))
	}
	if !rep.TimeTrusted {
		t.Fatal("RTCP SR must mark the timeline trusted")
	}
	keys := 0
	for _, sm := range rep.Samples {
		if sm.KeyFrame {
			keys++
		}
		if sm.TimeStart < 0 {
			t.Fatalf("unified time must be zero-based: %+v", sm)
		}
	}
	if keys != 3 {
		t.Fatalf("key frames = %d, want 3", keys)
	}
}

func TestReorderRecoversSameMapping(t *testing.T) {
	g := fixture.NewGenerator(7)
	clean := g.Normal(stream.CodecH264, 12, 4)
	cleanRep := Process(stream.CodecH264, clean.SSRC, clean.ClockRate, 16, toDatagrams(clean))

	shuffled := fixture.NewGenerator(7).Normal(stream.CodecH264, 12, 4)
	shuffled.ReorderAndLoss(3) // swaps only, no drops
	shufRep := Process(stream.CodecH264, shuffled.SSRC, shuffled.ClockRate, 16, toDatagrams(shuffled))

	if len(cleanRep.Samples) != len(shufRep.Samples) {
		t.Fatalf("sample count diverges after reorder: %d vs %d",
			len(cleanRep.Samples), len(shufRep.Samples))
	}
	for i := range cleanRep.Samples {
		a, b := cleanRep.Samples[i], shufRep.Samples[i]
		if a.FirstSeq != b.FirstSeq || a.LastSeq != b.LastSeq ||
			a.ByteOffset != b.ByteOffset || a.ByteLength != b.ByteLength ||
			a.TimeStart != b.TimeStart || a.KeyFrame != b.KeyFrame {
			t.Fatalf("sample %d mapping diverges:\n clean=%+v\n shuf =%+v", i, a, b)
		}
	}
	if shufRep.ReorderStats.MissingPackets != 0 {
		t.Fatal("pure reorder must not report loss")
	}
}

func TestLossAndTruncatedHeadAreLocalized(t *testing.T) {
	s := fixture.NewGenerator(7).Normal(stream.CodecH264, 12, 4)
	// Capture target seqs before any mutation reorders the packet slice.
	dropped := []uint16{s.Packets[4].SequenceNumber, s.Packets[5].SequenceNumber}
	headSeq := s.Packets[7].SequenceNumber
	s.ReorderAndLoss(2, dropped...)
	if !s.InjectTruncatedHeader(headSeq) {
		t.Fatal("failed to inject truncated header")
	}
	rep := Process(stream.CodecH264, s.SSRC, s.ClockRate, 16, toDatagrams(s))

	var sawLoss, sawHead bool
	for _, f := range rep.Faults {
		switch f.Class {
		case fault.ClassMissingPacket:
			sawLoss = true
			if f.Seq.First != uint64(dropped[0]) || f.Seq.Last != uint64(dropped[1]) {
				t.Fatalf("loss location = %+v, want seqs %d..%d", f.Seq, dropped[0], dropped[1])
			}
		case fault.ClassTruncatedHead:
			sawHead = true
		}
	}
	if !sawLoss || !sawHead {
		t.Fatalf("expected loss+truncated-head faults, got %+v", rep.Faults)
	}
	// Input damage must not abort the report: later samples survive.
	if len(rep.Samples) < 8 {
		t.Fatalf("post-damage samples missing: %d", len(rep.Samples))
	}
}

func TestExitCodeSemanticsOfFaultPresence(t *testing.T) {
	// A damaged-but-complete report still carries every surviving sample.
	s := fixture.NewGenerator(7).Normal(stream.CodecH264, 10, 5)
	s.ReorderAndLoss(2, s.Packets[3].SequenceNumber)
	rep := Process(stream.CodecH264, s.SSRC, s.ClockRate, 16, toDatagrams(s))
	if len(rep.Faults) == 0 || len(rep.Samples) == 0 {
		t.Fatal("damaged input must yield faults together with surviving samples")
	}
}

func TestThreeInvalidationPaths(t *testing.T) {
	base := fixture.NewGenerator(7).Normal(stream.CodecH264, 8, 4)
	rep := Process(stream.CodecH264, base.SSRC, base.ClockRate, 16, toDatagrams(base))

	content := cache.ContentHash(fixture.PayloadBytes(base.Packets))
	container := cache.ContainerHash("H264", fixture.HeaderBytes(base.Packets))
	metadata := cache.MetadataHash([]byte("labels-v1"))

	coord := NewCoordinator()
	mn := BuildManifest(Ingestion{
		Report: rep, ContentHash: content, ContainerHash: container,
		MetadataHash: metadata, TrackID: base.SSRC,
	}, 14400)
	coord.InitialApply(Ingestion{ContentHash: content, ContainerHash: container,
		MetadataHash: metadata}, mn)

	// Baseline: a content lookup hits.
	if _, ok := coord.LookupContent(content); !ok {
		t.Fatal("initial content must be cached")
	}

	// 1. Same-path content replacement: only content cache invalidated.
	replaced := fixture.NewGenerator(7).Normal(stream.CodecH264, 8, 4)
	replaced.Packets[1].Payload[0] ^= 0xFF
	newContent := cache.ContentHash(fixture.PayloadBytes(replaced.Packets))
	if newContent == content {
		t.Fatal("test setup: replacement did not change content hash")
	}
	if !coord.EvictContent(content) {
		t.Fatal("content replacement must invalidate the old content key")
	}
	rep2 := Process(stream.CodecH264, replaced.SSRC, replaced.ClockRate, 16, toDatagrams(replaced))
	mn2 := BuildManifest(Ingestion{Report: rep2, ContentHash: newContent,
		ContainerHash: container, MetadataHash: metadata, TrackID: replaced.SSRC}, 14400)
	st := coord.ContentReplaced(Ingestion{ContentHash: newContent, ContainerHash: container,
		MetadataHash: metadata}, mn2)
	if coord.CacheHas(content) {
		t.Fatal("old content must be gone after replacement")
	}
	if st.CacheInvalidations != 1 {
		t.Fatalf("content invalidations = %d, want 1", st.CacheInvalidations)
	}

	// 2. Container change: content stays hot, index rebuilds.
	newContainer := cache.ContainerHash("H265", fixture.HeaderBytes(replaced.Packets))
	coord.ContainerChanged(Ingestion{ContentHash: newContent, ContainerHash: newContainer,
		MetadataHash: metadata}, mn2)
	if _, ok := coord.LookupContent(newContent); !ok {
		t.Fatal("container change must preserve the content cache")
	}
	c, _ := coord.IndexIdentity()
	if c != newContainer {
		t.Fatalf("index container identity = %s, want %s", c, newContainer)
	}

	// 3. Metadata-only update: content cache hit, index rebuild again.
	newMeta := cache.MetadataHash([]byte("labels-v2"))
	coord.MetadataUpdated(Ingestion{ContentHash: newContent, ContainerHash: newContainer,
		MetadataHash: newMeta}, mn2)
	if _, ok := coord.LookupContent(newContent); !ok {
		t.Fatal("metadata update must keep content cache serving hits")
	}
	_, m := coord.IndexIdentity()
	if m != newMeta {
		t.Fatalf("index metadata identity = %s, want %s", m, newMeta)
	}
	final := coord.Stats()
	if final.CacheInvalidations != 1 {
		t.Fatalf("only one content invalidation allowed, got %d", final.CacheInvalidations)
	}
	// initial + content replacement + container change + metadata update.
	if final.IndexBuilds != 4 {
		t.Fatalf("index builds = %d, want 4", final.IndexBuilds)
	}
	// The distinguishing assertion: container and metadata changes never
	// evict content, so there is still exactly one invalidation.
	if final.CacheHits < 3 {
		t.Fatalf("content cache must keep hitting: %+v", final)
	}
}
