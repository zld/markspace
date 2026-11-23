package pipeline_test

import (
	"context"
	"strconv"
	"testing"

	"mediad/internal/merr"
	"mediad/internal/pipeline"
	"mediad/internal/testkit"
)

// TestFaultPropagation documents which of the first four fault classes carry
// an observable error from the parse/transport stage through to the index or
// the fan-in cross-check. The requirement is at least three of the four.
func TestFaultPropagation(t *testing.T) {
	type outcome struct {
		propagated bool
		detail     string
	}
	results := map[string]outcome{}

	// 1. Truncated cluster: the parse error must reach the index as a
	// recovery cursor and an error_code.
	trunc := runFixture(t, testkit.FaultClusterTruncate, pipeline.Options{})
	results["cluster-truncate"] = outcome{
		propagated: trunc.Summary.ErrorCode == string(merr.CodeTruncatedCluster) &&
			trunc.Summary.RecoverablePrefixByte > 0,
		detail: "error_code=" + trunc.Summary.ErrorCode,
	}

	// 2. Timestamp wrap: the counter must be unwrapped, which is observable
	// as unwrap decisions plus a still-monotonic timeline.
	wrap := runFixture(t, testkit.FaultTimestampWrap, pipeline.Options{})
	results["timestamp-wrap"] = outcome{
		propagated: wrap.Summary.Decisions["unwrap"] > 0 && wrap.Summary.TimestampMonotonic,
		detail:     "unwrap decisions=" + itoa(wrap.Summary.Decisions["unwrap"]),
	}

	// 3. RTP loss: the loss must surface as reduced packet recovery and as
	// fan-in dropouts.
	loss := runFixture(t, testkit.FaultRTPLoss, demoOptions(17))
	results["rtp-loss"] = outcome{
		propagated: loss.Summary.PacketRecoveryRate < 1 && loss.Summary.FanInDropouts > 0,
		detail: "rate=" + ftoa(loss.Summary.PacketRecoveryRate) +
			" dropouts=" + itoa(loss.Summary.FanInDropouts),
	}

	// 4. Unknown-size nesting is an encoding form, not a fault; it must be
	// parsed without damage and produce the full index.
	nest := runFixture(t, testkit.FaultUnknownSizeNest, pipeline.Options{})
	results["unknown-size-nest"] = outcome{
		propagated: nest.Damage == nil && nest.Summary.IndexEntries >= 10,
		detail:     "index_entries=" + itoa(nest.Summary.IndexEntries),
	}

	propagated := 0
	for _, name := range []string{"cluster-truncate", "timestamp-wrap", "rtp-loss", "unknown-size-nest"} {
		r := results[name]
		if r.propagated {
			propagated++
		}
		t.Logf("%-20s propagated=%-5v %s", name, r.propagated, r.detail)
	}
	if propagated < 3 {
		t.Fatalf("only %d of 4 fault classes reached index or fan-in, want at least 3", propagated)
	}
}

func itoa(v int) string     { return strconv.Itoa(v) }
func ftoa(v float64) string { return strconv.FormatFloat(v, 'f', 4, 64) }

func TestSummaryHashIsReproducible(t *testing.T) {
	a := runFixture(t, testkit.FaultNone, pipeline.Options{})
	b := runFixture(t, testkit.FaultNone, pipeline.Options{})
	if a.Summary.SummaryHash != b.Summary.SummaryHash {
		t.Fatalf("summary hash differs: %s vs %s",
			a.Summary.SummaryHash, b.Summary.SummaryHash)
	}
	if a.Index.Index.Digest != b.Index.Index.Digest {
		t.Fatal("index digest differs across identical runs")
	}
}

func TestIndexDigestStableAcrossFaultModes(t *testing.T) {
	// The same prefix must hash the same whether the tail was truncated or
	// absent. Truncating the healthy document at the cursor offset proves the
	// cursor is a real parse boundary.
	full := mustFixture(t, testkit.FaultClusterTruncate)
	out := runFixture(t, testkit.FaultClusterTruncate, pipeline.Options{})
	cut := out.Index.Cursor.ConfirmedBytes
	if cut <= 0 || cut >= int64(len(full)) {
		t.Fatalf("cursor %d outside input", cut)
	}
	prefix := full[:cut]
	again, err := pipeline.Run(context.Background(), prefix, pipeline.Options{})
	if err != nil {
		t.Fatalf("prefix run: %v", err)
	}
	if len(again.Index.Index.Entries) != len(out.Index.Index.Entries) {
		t.Fatalf("prefix entries = %d, original = %d",
			len(again.Index.Index.Entries), len(out.Index.Index.Entries))
	}
	if again.Index.Index.Digest != out.Index.Index.Digest {
		t.Fatalf("prefix digest %s != original %s",
			again.Index.Index.Digest, out.Index.Index.Digest)
	}
}
