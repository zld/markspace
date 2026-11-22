package segmenter

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"tsrebuild/internal/fixture"
	"tsrebuild/internal/inspect"
	"tsrebuild/internal/pipeline"
	"tsrebuild/internal/tsreader"
)

// statsByPID flattens inspect interval groups into per-PID totals.
func statsByPID(res inspect.Result) map[uint16]int64 {
	out := map[uint16]int64{}
	out[0] = res.PAT.Packets
	for _, g := range res.Intervals {
		for _, s := range g {
			out[s.PID] += s.Packets
		}
	}
	return out
}

func TestSegmenterOutputMatchesInspectStats(t *testing.T) {
	var gen bytes.Buffer
	if _, err := fixture.Generate(&gen, fixture.Options{Scenario: fixture.ScenarioRemap}); err != nil {
		t.Fatal(err)
	}
	data := gen.Bytes() // snapshot: segmenter consumes its own reader below

	var out bytes.Buffer
	rep, err := Run(bytes.NewReader(data), &out, 1)
	if err != nil {
		t.Fatal(err)
	}

	// The output must itself be a sequence of synchronized 188-byte packets.
	outData := out.Bytes()
	if len(outData)%tsreader.PacketSize != 0 {
		t.Fatalf("output %d bytes not a multiple of 188", len(outData))
	}
	for off := 0; off < len(outData); off += tsreader.PacketSize {
		if outData[off] != tsreader.SyncByte {
			t.Fatalf("output packet %d missing sync byte", off/tsreader.PacketSize)
		}
	}
	if int64(len(outData)/tsreader.PacketSize) != rep.WrittenPackets {
		t.Fatalf("written count %d does not match output bytes %d", rep.WrittenPackets, len(outData)/188)
	}

	// Independently derive expected per-PID totals from the inspect pipeline.
	eng := pipeline.New(bytes.NewReader(data), 1)
	for {
		if _, err := eng.Next(); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
	}
	expected := statsByPID(eng.Stats())
	got := map[uint16]int64{0: rep.PATPackets}
	var intervalSum int64
	for _, iv := range rep.Intervals {
		for _, c := range iv.Written {
			got[c.PID] += c.Packets
			intervalSum += c.Packets
		}
	}
	if intervalSum+rep.PATPackets != rep.WrittenPackets {
		t.Fatalf("interval counts (%d) + PAT (%d) != written (%d)", intervalSum, rep.PATPackets, rep.WrittenPackets)
	}
	for pid, want := range expected {
		if got[pid] != want {
			t.Fatalf("PID %d: segmenter wrote %d, inspect counted %d", pid, got[pid], want)
		}
	}
	for pid, have := range got {
		if _, ok := expected[pid]; !ok {
			t.Fatalf("segmenter emitted PID %d absent from inspect stats (%d)", pid, have)
		}
	}
}

func TestSegmenterIntervalsSwitchAtBoundary(t *testing.T) {
	var src bytes.Buffer
	if _, err := fixture.Generate(&src, fixture.Options{Scenario: fixture.ScenarioRemap}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	rep, err := Run(&src, &out, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Intervals) != 2 {
		t.Fatalf("expected 2 interval reports, got %d", len(rep.Intervals))
	}
	iv0, iv1 := rep.Intervals[0], rep.Intervals[1]
	if iv0.EndPacket != iv1.StartPacket {
		t.Fatal("intervals must meet at the remap boundary")
	}
	if hasPID(iv0, fixture.PIDVideoV1) || !hasPID(iv0, fixture.PIDVideoV0) {
		t.Fatalf("interval 0 PIDs wrong: %+v", iv0.Written)
	}
	if hasPID(iv1, fixture.PIDVideoV0) || !hasPID(iv1, fixture.PIDVideoV1) {
		t.Fatalf("interval 1 PIDs wrong: %+v", iv1.Written)
	}
	// Audio PID 257 exists in both intervals, counted separately on each side.
	if !hasPID(iv0, fixture.PIDAudio) || !hasPID(iv1, fixture.PIDAudio) {
		t.Fatal("audio must be selected on both sides of the switch")
	}
}

func TestSegmenterRejectsUnknownProgram(t *testing.T) {
	var src bytes.Buffer
	if _, err := fixture.Generate(&src, fixture.Options{Scenario: fixture.ScenarioRemap}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := Run(&src, &out, 99); !errors.Is(err, ErrProgramNotFound{Program: 99}) {
		t.Fatalf("expected ErrProgramNotFound, got %v", err)
	}
	if out.Len() != 0 {
		t.Fatal("nothing should be written for a program that never maps")
	}
}

func TestSelectorRules(t *testing.T) {
	sel := NewSelector(1)
	if sel.Wants(&pipeline.PacketEvent{Packet: &tsreader.Packet{PID: 0}}) != true {
		t.Fatal("PAT packets are always selected")
	}
	if sel.Wants(&pipeline.PacketEvent{Packet: &tsreader.Packet{PID: 256}}) != false {
		t.Fatal("packet with no active interval must not be selected")
	}
}

func hasPID(iv IntervalReport, pid uint16) bool {
	for _, c := range iv.Written {
		if c.PID == pid {
			return true
		}
	}
	return false
}
