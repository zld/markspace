package stats

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"relaylab/internal/forward"
)

func TestReportJSONShape(t *testing.T) {
	r := NewReport(42)
	r.CandidatePairs = append(r.CandidatePairs, CandidatePair{
		LocalAddr: "127.0.0.1:50001", RemoteAddr: "127.0.0.1:50002",
		Protocol: "udp", Type: "host",
	})
	r.SentPackets = 1200
	r.FillRelay(forward.RelayStats{Enqueued: 1200, Forwarded: 1100, Dropped: 100, Duplicates: 3, Reordered: 5, Malformed: 0}, 0)
	j := NewJitterTracker(8000)
	j.Observe(0, 0)
	j.Observe(160, 170)
	r.FillJitter(j)
	r.Finalize("ok", true, 5*time.Second)

	var buf bytes.Buffer
	if err := r.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	raw := buf.String()
	for _, key := range []string{
		"candidate_pairs", "sent_packets", "forwarded_packets", "dropped_packets",
		"duplicate_packets", "reordered_packets", "jitter_rtp_ticks",
		"jitter_ms", "shutdown_clean",
	} {
		if !strings.Contains(raw, key) {
			t.Errorf("report missing key %s", key)
		}
	}
	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if decoded["shutdown_clean"] != true {
		t.Error("shutdown_clean must render as JSON true")
	}
	if _, ok := decoded["candidate_pairs"].([]any); !ok {
		t.Error("candidate_pairs must be a JSON array")
	}
}

func TestReportEmptySlicesRenderAsArray(t *testing.T) {
	var buf bytes.Buffer
	if err := NewReport(1).WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"candidate_pairs": []`) {
		t.Fatalf("empty slice rendered wrong: %s", buf.String())
	}
}
