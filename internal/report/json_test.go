package report

import (
	"bytes"
	"encoding/json"
	"testing"

	"looprelay/internal/metrics"
)

func sampleReport() Report {
	return Report{
		Status:             StatusOK,
		Command:            "demo",
		RequestID:          1,
		Listen:             "127.0.0.1:50123",
		CandidatePairCount: 2,
		Links: []Link{
			{Name: "ingress", Connected: true, LocalAddress: "127.0.0.1", LocalPort: 50000, RemoteAddress: "127.0.0.1", RemotePort: 50001},
			{Name: "egress", Connected: true, LocalAddress: "127.0.0.1", LocalPort: 50002, RemoteAddress: "127.0.0.1", RemotePort: 50003},
		},
		GeneratedPackets: 240,
		SkippedPackets:   6,
		SentPackets:      234,
		ForwardedPackets: 234,
		ReceivedPackets:  234,
		LostPackets:      0,
		JitterMS:         metrics.JitterSummary{Status: metrics.JitterStatusOK, Samples: 233, Mean: 0.42, P95: 0.9, Max: 1.2},
		Sequence:         SequenceSummary{Received: 234, ExpectedSpan: 234, FirstSequence: 1, LastSequence: 240},
		Timestamps:       TimestampSummary{Received: 234, FirstTimestamp: 0, LastTimestamp: 430200},
		Checks:           []Check{{Name: "candidate_pairs", OK: true}},
		DurationMS:       4800,
	}
}

func TestWriteJSONRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, sampleReport()); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	required := []string{
		"candidate_pair_count", "generated_packets", "forwarded_packets",
		"received_packets", "lost_packets", "jitter_ms", "status",
	}
	for _, key := range required {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("missing key %q", key)
		}
	}
	jitter, ok := decoded["jitter_ms"].(map[string]any)
	if !ok {
		t.Fatal("jitter_ms is not an object")
	}
	for _, key := range []string{"mean", "p95", "max"} {
		if _, ok := jitter[key]; !ok {
			t.Fatalf("missing jitter_ms.%s", key)
		}
	}
}

func TestWriteJSONIsDeterministic(t *testing.T) {
	var first, second bytes.Buffer
	if err := WriteJSON(&first, sampleReport()); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(&second, sampleReport()); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatalf("output drifted:\n%s\n---\n%s", first.String(), second.String())
	}
	if first.Bytes()[first.Len()-1] != '\n' {
		t.Fatal("expected trailing newline")
	}
}

func TestPassedRequiresAllChecks(t *testing.T) {
	r := sampleReport()
	if !r.Passed() {
		t.Fatal("expected passing report")
	}
	r.Checks = append(r.Checks, Check{Name: "packet_count", OK: false, Detail: "short"})
	if r.Passed() {
		t.Fatal("failed check must fail the report")
	}
	r = sampleReport()
	r.Status = StatusFailed
	if r.Passed() {
		t.Fatal("failed status must fail the report")
	}
}
