package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"tsrebuild/internal/inspect"
	"tsrebuild/internal/program"
	"tsrebuild/internal/psi"
	"tsrebuild/internal/segmenter"
)

func buildTimeline(t *testing.T) *program.Timeline {
	t.Helper()
	tl := program.NewTimeline(7)
	tl.ApplyPAT(&psi.PAT{TransportStreamID: 7, Version: 0, Programs: []psi.PATProgram{{1, 100}}})
	pmt0 := &psi.PMT{ProgramNumber: 1, PCRPID: 256, Version: 0, Streams: []psi.ElementaryStream{
		{PID: 256, StreamType: 0x1b, Kind: psi.StreamVideo},
		{PID: 257, StreamType: 0x0f, Kind: psi.StreamAudio},
	}}
	if _, err := tl.ApplyMapping(1, 100, 0, 0, pmt0, 10); err != nil {
		t.Fatal(err)
	}
	tl.ApplyPAT(&psi.PAT{TransportStreamID: 7, Version: 1, Programs: []psi.PATProgram{{1, 100}}})
	pmt1 := &psi.PMT{ProgramNumber: 1, PCRPID: 300, Version: 1, Streams: []psi.ElementaryStream{
		{PID: 300, StreamType: 0x1b, Kind: psi.StreamVideo},
		{PID: 257, StreamType: 0x0f, Kind: psi.StreamAudio},
	}}
	if _, err := tl.ApplyMapping(1, 100, 1, 1, pmt1, 40); err != nil {
		t.Fatal(err)
	}
	return tl
}

func sampleStats() inspect.Result {
	return inspect.Result{
		ProgramNumber: 1,
		PAT:           inspect.PIDStats{PID: 0, Kind: "psi", IntervalStart: 0, IntervalEnd: -1, Packets: 10},
		Intervals: [][]inspect.PIDStats{
			{
				{PID: 100, Kind: "psi", IntervalStart: 10, IntervalEnd: 40, Packets: 5},
				{PID: 256, Kind: "video", IntervalStart: 10, IntervalEnd: 40, Packets: 5},
				{PID: 257, Kind: "audio", IntervalStart: 10, IntervalEnd: 40, Packets: 5, ContinuityGaps: 1},
			},
			{
				{PID: 100, Kind: "psi", IntervalStart: 40, IntervalEnd: -1, Packets: 5},
				{PID: 257, Kind: "audio", IntervalStart: 40, IntervalEnd: -1, Packets: 5},
				{PID: 300, Kind: "video", IntervalStart: 40, IntervalEnd: -1, Packets: 5},
			},
		},
	}
}

func TestBuildInspectStructure(t *testing.T) {
	tl := buildTimeline(t)
	rep := BuildInspect(tl, sampleStats(), 100, 1, 1)

	if rep.TransportStreamID != 7 || rep.PATVersionChanges != 1 || rep.PMTVersionChanges != 1 {
		t.Fatalf("header counters wrong: %+v", rep)
	}
	if rep.RejectedCRC.PAT != 1 || rep.RejectedCRC.PMT != 1 {
		t.Fatalf("rejected CRC wrong: %+v", rep.RejectedCRC)
	}
	if len(rep.MappingIntervals) != 2 {
		t.Fatalf("expected 2 mapping intervals, got %d", len(rep.MappingIntervals))
	}
	iv0, iv1 := rep.MappingIntervals[0], rep.MappingIntervals[1]
	if iv0.Streams[0].PID != 256 || iv1.Streams[0].PID != 300 {
		t.Fatalf("remap not reflected: %+v %+v", iv0, iv1)
	}
	if iv0.PCRPID != 256 || iv1.PCRPID != 300 {
		t.Fatalf("PCR PID wrong")
	}
	// PAT + 3 stats per interval.
	if len(rep.PIDStats) != 7 {
		t.Fatalf("expected 7 pid_stats rows (PAT + 6), got %d", len(rep.PIDStats))
	}
	if rep.PIDStats[0].PID != 0 {
		t.Fatal("PAT must be the first pid_stats row")
	}
	// Audio appears once per interval with distinct ranges.
	var audioRows []PIDStatsJSON
	for _, r := range rep.PIDStats {
		if r.PID == 257 {
			audioRows = append(audioRows, r)
		}
	}
	if len(audioRows) != 2 || audioRows[0].IntervalEnd != 40 || audioRows[1].IntervalStart != 40 {
		t.Fatalf("audio must be counted per interval: %+v", audioRows)
	}
	if audioRows[0].ContinuityGaps != 1 {
		t.Fatal("continuity gap not carried into JSON")
	}
}

func TestJSONIsStable(t *testing.T) {
	tl := buildTimeline(t)
	rep := BuildInspect(tl, sampleStats(), 100, 0, 0)
	var first, second bytes.Buffer
	if err := WriteJSON(&first, rep); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(&second, rep); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Fatal("identical input produced different JSON")
	}
	if !strings.HasSuffix(first.String(), "}\n") {
		t.Fatal("JSON output must end with a newline")
	}
	var decoded StreamReport
	if err := json.Unmarshal(first.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if decoded.TotalPackets != 100 {
		t.Fatal("round-trip lost a field")
	}
}

func TestBuildSegment(t *testing.T) {
	rep := BuildSegment(&segmenter.Report{
		ProgramNumber: 1, InputPackets: 100, WrittenPackets: 30, PATPackets: 10,
		Intervals: []segmenter.IntervalReport{
			{StartPacket: 10, EndPacket: 40, PATVersion: 0, PMTVersion: 0, PMTPID: 100,
				Written: []segmenter.PIDCount{{PID: 100, Kind: "psi", Packets: 5}, {PID: 256, Kind: "video", Packets: 5}}},
			{StartPacket: 40, EndPacket: -1, PATVersion: 1, PMTVersion: 1, PMTPID: 100,
				Written: []segmenter.PIDCount{{PID: 300, Kind: "video", Packets: 5}}},
		},
	})
	if rep.WrittenPackets != 30 || rep.PATPackets != 10 || len(rep.Intervals) != 2 {
		t.Fatalf("segment report fields wrong: %+v", rep)
	}
	if rep.Intervals[1].Written[0].PID != 300 {
		t.Fatal("segment interval contents wrong")
	}
	var buf bytes.Buffer
	if err := WriteJSON(&buf, rep); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"written_packets": 30`) {
		t.Fatalf("expected JSON field missing in %s", buf.String())
	}
}
