package pipeline

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"tsrebuild/internal/fixture"
	"tsrebuild/internal/inspect"
)

func drain(t *testing.T, data []byte, program uint16) *Engine {
	t.Helper()
	eng := New(bytes.NewReader(data), program)
	var changes int
	for {
		ev, err := eng.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("pipeline error: %v", err)
		}
		if ev.MappingChanged {
			changes++
		}
	}
	if changes < 1 {
		t.Fatal("expected at least one mapping-change event when the first mapping commits")
	}
	return eng
}

func TestEngineRemapLossTimeline(t *testing.T) {
	var buf bytes.Buffer
	m, err := fixture.Generate(&buf, fixture.Options{Scenario: fixture.ScenarioRemap})
	if err != nil {
		t.Fatal(err)
	}
	eng := drain(t, buf.Bytes(), 1)
	tl := eng.Timeline()
	if tl.PATVersionChanges() != 1 || tl.PMTVersionChanges(1) != 1 {
		t.Fatalf("version changes wrong: pat=%d pmt=%d", tl.PATVersionChanges(), tl.PMTVersionChanges(1))
	}
	if len(tl.Intervals(1)) != 2 {
		t.Fatalf("expected 2 intervals, got %d", len(tl.Intervals(1)))
	}
	if eng.PacketCount() != m.TotalPackets {
		t.Fatalf("consumed %d packets, manifest says %d", eng.PacketCount(), m.TotalPackets)
	}
}

func TestEngineStatsMatchManifest(t *testing.T) {
	var buf bytes.Buffer
	m, err := fixture.Generate(&buf, fixture.Options{Scenario: fixture.ScenarioRemap})
	if err != nil {
		t.Fatal(err)
	}
	eng := drain(t, buf.Bytes(), 1)
	res := eng.Stats()
	if len(res.Intervals) != 2 {
		t.Fatalf("expected 2 stat groups, got %d", len(res.Intervals))
	}

	find := func(group []inspect.PIDStats, pid uint16) bool {
		for _, s := range group {
			if s.PID == pid {
				return true
			}
		}
		return false
	}
	if !find(res.Intervals[0], fixture.PIDVideoV0) || find(res.Intervals[0], fixture.PIDVideoV1) {
		t.Fatalf("pre-switch PIDs wrong: %v", pidsOf(res.Intervals[0]))
	}
	if !find(res.Intervals[1], fixture.PIDVideoV1) || find(res.Intervals[1], fixture.PIDVideoV0) {
		t.Fatalf("post-switch PIDs wrong: %v", pidsOf(res.Intervals[1]))
	}
	var audioGaps, audioPackets int64
	for _, g := range res.Intervals {
		for _, s := range g {
			if s.PID == fixture.PIDAudio {
				audioGaps += s.ContinuityGaps
				audioPackets += s.Packets
			}
		}
	}
	if audioGaps != int64(m.Phases[1].AudioGaps) || audioPackets != 10 {
		t.Fatalf("audio stats wrong: gaps=%d packets=%d", audioGaps, audioPackets)
	}
}

func TestEngineBadCRCRejected(t *testing.T) {
	var buf bytes.Buffer
	if _, err := fixture.Generate(&buf, fixture.Options{Scenario: fixture.ScenarioBadCRC}); err != nil {
		t.Fatal(err)
	}
	eng := drain(t, buf.Bytes(), 1)
	pat, pmt := eng.RejectedCRC()
	if pat == 0 || pmt == 0 {
		t.Fatalf("bad CRC sections must be counted: pat=%d pmt=%d", pat, pmt)
	}
	if eng.Timeline().PATVersionChanges() != 0 || eng.Timeline().PMTVersionChanges(1) != 0 {
		t.Fatal("corrupt tables must not become versions")
	}
}

func pidsOf(g []inspect.PIDStats) []uint16 {
	out := make([]uint16, 0, len(g))
	for _, s := range g {
		out = append(out, s.PID)
	}
	return out
}
