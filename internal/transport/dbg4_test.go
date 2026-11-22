package transport_test

import (
	"testing"
	"time"

	"rtspcheck/internal/sim"
	"rtspcheck/internal/transport"
	"rtspcheck/testdata"
)

func TestDbg4(t *testing.T) {
	scenario, _ := testdata.New(testdata.ScenarioGenerated)
	srv, _ := sim.New(sim.Config{Packets: scenario.Packets, PacketInterval: 2 * time.Millisecond})
	srv.Start()
	defer srv.Close()
	res, _ := transport.RunInterleaved(transport.InterleavedConfig{
		ServerURL: srv.URL(), ExpectedPackets: len(scenario.Packets), RTPTimeout: 5 * time.Second,
	})
	for i, f := range res.Frames() {
		t.Logf("frame %d ts=%d complete=%v gaps=%d", i, f.Timestamp, f.Complete, f.GapCount)
	}
	t.Logf("gaps=%v", res.Gaps())
}
