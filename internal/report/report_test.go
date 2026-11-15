package report

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"streamlab/internal/jitter"
	"streamlab/internal/receive"
	"streamlab/internal/wire"
)

func TestPercentile(t *testing.T) {
	if got := percentile(nil, 95); got != 0 {
		t.Fatalf("empty percentile %v", got)
	}
	xs := []float64{1, 2, 3, 4, 5}
	if got := percentile(xs, 50); got != 3 {
		t.Fatalf("p50 %v want 3", got)
	}
	if got := percentile(xs, 100); got != 5 {
		t.Fatalf("p100 %v want 5", got)
	}
}

func TestSummarizeFields(t *testing.T) {
	p := receive.NewPipeline(20, 1, 4,
		jitter.Config{FrameMS: 20, Target: 2, MinLevel: 1, MaxLevel: 8}, 16, 3)
	mk := func(seq uint16) []byte {
		return wire.Encode(&wire.Packet{StreamID: 1, Seq: seq,
			Timestamp: uint32(seq) * 20, Samples: []int16{100, 100, 100, 100}})
	}
	p.OnDatagram(mk(0), 30)
	p.OnDatagram(mk(1), 50)
	p.OnDatagram([]byte{0xff}, 55) // bad packet, counted not fatal
	for t := int64(0); t < 500; t++ {
		p.AdvanceTo(t)
	}
	s := Summarize(p)
	if s.ReceivedPackets != 2 || s.DroppedPackets["too_short"] != 1 {
		t.Fatalf("counts: %+v", s)
	}
	if len(s.BufferWatermarks) != len(p.Events) || len(s.BufferWatermarks) == 0 {
		t.Fatalf("watermarks %d events %d", len(s.BufferWatermarks), len(p.Events))
	}
	if s.ConcealedFrames == 0 || s.ConcealRunsMax == 0 {
		t.Fatalf("expected concealment after stream end: %+v", s)
	}
	for _, v := range []float64{s.BoundaryEnergyJumpP95, s.LatencyMSP50, s.LatencyMSP95, s.JitterMS} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Fatalf("non-finite value in %+v", s)
		}
	}
}

func TestWriteJSONStableFields(t *testing.T) {
	var buf bytes.Buffer
	s := Summary{ReceivedPackets: 1, DroppedPackets: map[string]int{"late": 2},
		BufferWatermarks: []int{1, 2}}
	if err := WriteJSON(&buf, s); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"received_packets", "dropped_packets", "concealed_frames",
		"conceal_runs_max", "boundary_energy_jump_p95", "buffer_watermarks",
	} {
		if !strings.Contains(buf.String(), `"`+field+`"`) {
			t.Errorf("missing field %q in %s", field, buf.String())
		}
	}
	var back Summary
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
}
