package report_test

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestJSONStableAndSorted(t *testing.T) {
	var data []byte
	patCC, p2CC, p1CC, esA, esB := uint8(0), uint8(0), uint8(0), uint8(0), uint8(0)
	add := func(ps [][]byte) {
		for _, p := range ps {
			data = append(data, p...)
		}
	}
	// PAT lists program 2 before program 1 on purpose.
	add(sectionPackets(0, &patCC, pat(0, [2]uint16{2, 0x1001}, [2]uint16{1, 0x1000})))
	add(sectionPackets(0x1001, &p2CC, pmt(2, 0, 0x200, [2]uint16{0x0f, 0x201}, [2]uint16{0x1b, 0x200})))
	add(sectionPackets(0x1000, &p1CC, pmt(1, 0, 0x100, [2]uint16{0x0f, 0x101}, [2]uint16{0x1b, 0x100})))
	add(esPackets(0x100, &esA, 3))
	add(esPackets(0x200, &esB, 4))
	// Reversed PID order must not affect output.
	data2 := append([]byte(nil), data...)

	first := render(t, data, 1)
	if second := render(t, data2, 1); !bytes.Equal(first, second) {
		t.Fatal("JSON differs between identical runs")
	}

	var doc struct {
		PATVersionChanges int `json:"pat_version_changes"`
		PMTVersionChanges int `json:"pmt_version_changes"`
		PIDMappings       []struct {
			ProgramNumber uint16 `json:"program_number"`
			PID           uint16 `json:"pid"`
			StreamType    uint8  `json:"stream_type"`
			PacketCount   int64  `json:"packet_count"`
		} `json:"pid_mappings"`
		ElementaryStreams []struct {
			ProgramNumber uint16 `json:"program_number"`
			PID           uint16 `json:"pid"`
		} `json:"elementary_streams"`
		Anomalies map[string]int64 `json:"anomalies"`
	}
	if err := json.Unmarshal(first, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.PATVersionChanges != 0 || doc.PMTVersionChanges != 0 {
		t.Fatalf("unexpected version change counts: %d %d", doc.PATVersionChanges, doc.PMTVersionChanges)
	}
	if len(doc.PIDMappings) != 4 || len(doc.ElementaryStreams) != 4 {
		t.Fatalf("mappings=%d es=%d", len(doc.PIDMappings), len(doc.ElementaryStreams))
	}
	// Sorted by program, then PID.
	prev := doc.PIDMappings[0]
	for _, m := range doc.PIDMappings {
		if m.ProgramNumber < prev.ProgramNumber || (m.ProgramNumber == prev.ProgramNumber && m.PID < prev.PID) {
			t.Fatal("pid_mappings not sorted")
		}
		prev = m
	}
	for _, key := range []string{"duplicates", "counter_jumps", "transport_errors", "unjudgeable"} {
		if _, ok := doc.Anomalies[key]; !ok {
			t.Fatalf("missing anomaly key %q", key)
		}
	}
}
