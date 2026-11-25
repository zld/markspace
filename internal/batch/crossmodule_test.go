package batch_test

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"

	"replaycheck/internal/chunks"
	"replaycheck/internal/clock"
	"replaycheck/internal/events"
	"replaycheck/internal/fixture"
)

func TestCrossModuleWrapToEventAndChunks(t *testing.T) {
	start := uint32(math.MaxUint32 - 6000)
	records := make([]fixture.RTPRecordSpec, 0, 4)
	for i := 0; i < 4; i++ {
		records = append(records, fixture.RTPRecordSpec{
			Sequence: uint16(i + 1), Timestamp: start + uint32(i*3000), SSRC: 1,
		})
	}
	data, _, err := fixture.BuildRTPFile(records)
	if err != nil {
		t.Fatal(err)
	}
	packets, err := fixture.ParseRTPBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	report := clock.UnwrapRTP(fixture.Stamps(packets), 90000, 0)
	if report.WrapCount != 1 || !report.Monotonic() {
		t.Fatalf("wrap=%d monotonic=%v anomalies=%+v", report.WrapCount, report.Monotonic(), report.Anomalies)
	}

	frames := make([]clock.Frame, len(report.Samples))
	for i, s := range report.Samples {
		dur := report.Samples[min(i+1, len(report.Samples)-1)].NS - s.NS
		if i == len(report.Samples)-1 {
			dur = report.Samples[i].NS - report.Samples[i-1].NS
		}
		frames[i] = clock.Frame{Index: i, DTSRaw: s.Ticks, DTSNS: s.NS, DurationNS: dur}
	}
	tl := clock.Timeline{Frames: frames}
	ev := events.Event{ID: "wrap", StartNS: frames[2].DTSNS, EndNS: frames[3].DTSNS}
	cut, err := events.MapEvent(ev, tl)
	if err != nil {
		t.Fatal(err)
	}
	list := chunks.PlanContiguous(1, cut.CutStartNS, cut.CutEndNS, 2, nil)
	if errs := chunks.Validate(list, cut.CutStartNS, cut.CutEndNS); len(errs) != 0 {
		t.Fatalf("chunk validation failed: %+v", errs)
	}
}

func TestCrossModuleChunkBoundaryCorruptPacketIsLocated(t *testing.T) {
	dir := t.TempDir()
	set, err := fixture.GenerateSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	var corrupt fixture.Sample
	for _, s := range set.Samples {
		if s.Kind == fixture.KindChunkCorrupt {
			corrupt = s
		}
	}
	data, err := os.ReadFile(corrupt.Path)
	if err != nil {
		t.Fatal(err)
	}
	packets, perr := fixture.ParseRTPBytes(data)
	if perr == nil {
		t.Fatal("expected corrupt boundary packet parse error")
	}
	var ce *clock.Error
	if !errors.As(perr, &ce) || ce.Code != clock.CodeCorruptPacket || ce.Offset < 0 {
		t.Fatalf("corrupt error = %#v", perr)
	}
	if len(packets) != 4 {
		t.Fatalf("complete prefix packets = %d, want 4", len(packets))
	}

	// The next chunk starts at the corrupt boundary and overlaps the prior
	// complete chunk by 5 ms; chunks must identify sequence 2 and deviation.
	list := []chunks.Chunk{
		{Sequence: 1, StartNS: 0, EndNS: 100_000_000, Complete: true},
		{Sequence: 2, StartNS: 95_000_000, EndNS: 133_333_333, Offset: ce.Offset},
	}
	errs := chunks.Validate(list, 0, 133_333_333)
	if len(errs) == 0 || errs[0].Code != clock.CodeChunkOverlap {
		t.Fatalf("expected overlap at corrupt boundary, got %+v", errs)
	}
	if errs[0].ChunkIndex != 2 || errs[0].Offset != ce.Offset {
		t.Fatalf("chunk error location not preserved: %+v corruptOffset=%d", errs[0], ce.Offset)
	}
}

func TestGeneratedSetHasFiveFiles(t *testing.T) {
	set, err := fixture.GenerateSet(filepath.Join(t.TempDir(), "samples"))
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Samples) != 5 {
		t.Fatalf("samples = %d, want 5", len(set.Samples))
	}
}
