package metrics

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestP95AndRate(t *testing.T) {
	ds := make([]time.Duration, 100)
	for i := range ds {
		ds[i] = time.Duration(i+1) * time.Millisecond
	}
	if got := P95Milliseconds(ds); got < 95 || got > 96 {
		t.Fatalf("p95=%v", got)
	}
	s, pct := RecoverRate(4, 4)
	if s != "100%" || pct != 100 {
		t.Fatalf("rate=%s pct=%v", s, pct)
	}
}

func TestMaxAVSync(t *testing.T) {
	audio := []int64{5_000_000, 45_000_000}
	video := []int64{0, 40_000_000}
	if got := MaxAVSyncNS(audio, video); got != 5_000_000 {
		t.Fatalf("sync=%d", got)
	}
}

func TestReportJSONLContainsCommonFields(t *testing.T) {
	var buf bytes.Buffer
	r := Report{Case: "normal", OK: true, P95MS: 2, InFlightPeakBytes: 9}
	if err := r.WriteJSONL(&buf); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["p95_ms"] == nil || m["inflight_peak_bytes"] == nil {
		t.Fatalf("missing common metrics: %s", buf.String())
	}
}
