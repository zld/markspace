package pipeline_test

import (
	"testing"
	"time"

	"mediad/internal/fanout"
	"mediad/internal/merr"
	"mediad/internal/pipeline"
	"mediad/internal/testkit"
)

func TestClusterTruncationPropagatesThroughIndex(t *testing.T) {
	out := runFixture(t, testkit.FaultClusterTruncate, pipeline.Options{})
	if out.Damage == nil {
		t.Fatal("no damage recorded for truncated cluster")
	}
	if code := merr.CodeOf(out.Damage); code != merr.CodeTruncatedCluster {
		t.Fatalf("damage code = %s, want truncated_cluster", code)
	}
	s := out.Summary
	if s.ErrorCode != string(merr.CodeTruncatedCluster) {
		t.Fatalf("summary error_code = %q", s.ErrorCode)
	}
	if s.RecoverablePrefixByte <= 0 {
		t.Fatalf("recoverable_prefix_bytes = %d, want > 0", s.RecoverablePrefixByte)
	}
	if s.RecoverablePrefixByte > int64(len(mustFixture(t, testkit.FaultClusterTruncate))) {
		t.Fatal("recovery cursor points past the end of the input")
	}
	if s.IndexEntries == 0 {
		t.Fatal("no confirmed index prefix on damaged input")
	}
	// The parse error must still leave a usable timeline.
	if !s.TimestampMonotonic || !s.IndexKeyframesOnly {
		t.Fatal("damaged path violated a cross-stage invariant")
	}
}

func mustFixture(t *testing.T, fault string) []byte {
	t.Helper()
	f, err := testkit.BuildFaulted(fault, 42)
	if err != nil {
		t.Fatal(err)
	}
	return f.Data
}

func TestTimestampWrapKeepsStreamUsable(t *testing.T) {
	out := runFixture(t, testkit.FaultTimestampWrap, pipeline.Options{})
	s := out.Summary
	unwraps := s.Decisions["unwrap"]
	if unwraps == 0 {
		t.Fatalf("no unwrap decisions; decisions = %v", s.Decisions)
	}
	if !s.TimestampMonotonic {
		t.Fatal("timestamps regressed across the wrap")
	}
	if s.SyncErrorMs > 20 {
		t.Errorf("sync_error_ms = %.3f after wrap", s.SyncErrorMs)
	}
	// Unwrapping must not shorten the clip: the index must still cover the
	// whole recording.
	if s.IndexEntries < 10 {
		t.Errorf("only %d index entries after wrap", s.IndexEntries)
	}
}

func TestUnknownSizeNestingIsAccepted(t *testing.T) {
	out := runFixture(t, testkit.FaultUnknownSizeNest, pipeline.Options{})
	if out.Damage != nil {
		t.Fatalf("unknown-size masters reported as damage: %v", out.Damage)
	}
	if out.FileInfo.ClusterEvents == 0 {
		t.Fatal("no clusters parsed from unknown-size Segment")
	}
	if out.Summary.IndexEntries < 10 {
		t.Fatalf("index entries = %d", out.Summary.IndexEntries)
	}
}

func TestRTPLossIsCountedNotStalls(t *testing.T) {
	out := runFixture(t, testkit.FaultRTPLoss, demoOptions(17))
	s := out.Summary
	if s.PacketRecoveryRate >= 1 {
		t.Fatal("loss fault produced a perfect recovery rate")
	}
	if s.PacketRecoveryRate < 0.90 {
		t.Fatalf("recovery rate %.4f below target", s.PacketRecoveryRate)
	}
	if out.RTP.Lost == 0 {
		t.Fatal("no packets declared lost")
	}
	if out.RTP.MaxReorder > 64 {
		t.Fatalf("reorder distance %d exceeds the 64-packet window", out.RTP.MaxReorder)
	}
	// The merge must still have produced a timeline.
	if len(out.Merge.Blocks) == 0 {
		t.Fatal("merge produced no blocks under loss")
	}
}

func TestSlowConsumerCancellationIsolated(t *testing.T) {
	// Cancel the 1080p consumer a third of the way through; the other two
	// representations must still deliver every block.
	opts := demoOptions(0)
	opts.SlowConsumer = 120 * time.Millisecond
	opts.CancelLayer = "1080p"
	out := runFixture(t, testkit.FaultSlowConsumer, opts)

	stats := map[string]fanout.LayerStats{}
	for _, l := range out.LadderStats {
		stats[l.Name] = l
	}
	if !stats["1080p"].Canceled {
		t.Fatal("1080p layer was not canceled")
	}
	for _, name := range []string{"360p", "720p"} {
		if stats[name].Canceled {
			t.Fatalf("%s wrongly canceled", name)
		}
		if stats[name].Offered != out.Summary.Blocks {
			t.Fatalf("%s offered %d of %d blocks", name, stats[name].Offered, out.Summary.Blocks)
		}
	}
	if !out.Summary.LayersConsistent {
		t.Fatal("cancellation leaked across layers")
	}
	// The canceled layer's prefix still merges; the rest of the timeline comes
	// from the surviving representations.
	if len(out.Merge.Blocks) == 0 {
		t.Fatal("merge produced no blocks")
	}
	for _, b := range out.Merge.Blocks {
		if b.Rep == "1080p" && b.Seq >= uint64(out.Summary.Blocks/3) {
			t.Fatalf("merge used a block past the canceled layer's prefix")
		}
	}
}
