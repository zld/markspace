package metrics

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestP95NearestRank(t *testing.T) {
	samples := []time.Duration{
		1 * time.Millisecond, 2 * time.Millisecond, 3 * time.Millisecond,
		4 * time.Millisecond, 100 * time.Millisecond,
	}
	// ceil(.95*5)=5th sample => 100ms.
	if got := P95Millis(samples); got != 100 {
		t.Fatalf("P95=%v want 100", got)
	}
	if got := P95Millis(nil); got != 0 {
		t.Fatalf("empty P95=%v want 0", got)
	}
}

func TestRequiredKeysPrinted(t *testing.T) {
	r := Report{ManifestSHA256: "abc", ManifestSegments: 9}
	var buf bytes.Buffer
	r.PrintKeyValues(&buf)
	out := buf.String()
	for _, key := range []string{
		"av_sync_ms=", "loss_recovery_rate=", "p95_latency_ms=", "manifest_segments=9",
		"alignment_errors=", "first_blocked_dependency=none", "manifest_sha256=abc",
	} {
		if !strings.Contains(out, key) {
			t.Fatalf("output missing %q:\n%s", key, out)
		}
	}
}
