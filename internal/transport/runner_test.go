package transport_test

import (
	"testing"
	"time"

	"rtspcheck/internal/sim"
	"rtspcheck/internal/transport"
	"rtspcheck/testdata"
)

func runScenario(t *testing.T, scenarioName string, mode transport.Mode) *transport.Result {
	t.Helper()

	scenario, err := testdata.New(scenarioName)
	if err != nil {
		t.Fatal(err)
	}

	srv, err := sim.New(sim.Config{
		Packets:        scenario.Packets,
		PacketInterval: 2 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	var res *transport.Result
	switch mode {
	case transport.ModeUDP:
		res, err = transport.RunUDP(transport.UDPConfig{
			ServerURL:          srv.URL(),
			ExpectedPackets:    len(scenario.Packets),
			RTPTimeout:         5 * time.Second,
			DrainQuietPeriod:   200 * time.Millisecond,
			ExpectedLastSeq:    scenario.ExpectedTailSeq,
			HasExpectedLastSeq: true,
			SSRC:               scenario.SSRC,
		})
	case transport.ModeInterleaved:
		res, err = transport.RunInterleaved(transport.InterleavedConfig{
			ServerURL:          srv.URL(),
			ExpectedPackets:    len(scenario.Packets),
			RTPTimeout:         5 * time.Second,
			ExpectedLastSeq:    scenario.ExpectedTailSeq,
			HasExpectedLastSeq: true,
			SSRC:               scenario.SSRC,
		})
	default:
		t.Fatalf("unknown mode %q", mode)
	}
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestGeneratedScenarioBothTransports(t *testing.T) {
	for _, mode := range []transport.Mode{transport.ModeUDP, transport.ModeInterleaved} {
		t.Run(string(mode), func(t *testing.T) {
			res := runScenario(t, testdata.ScenarioGenerated, mode)
			if res.Failure() != nil {
				t.Fatalf("session failed: %v (stage %s/%s)",
					res.Failure(), res.FailureStage(), res.FailureMethod())
			}
			if got := len(res.Transitions()); got != 6 {
				t.Fatalf("state transitions = %d, want 6: %+v", got, res.Transitions())
			}
			if res.GapCount() != 1 {
				t.Fatalf("sequence gaps = %d, want 1 (%v)", res.GapCount(), res.Gaps())
			}
			complete, incomplete := res.FrameStats()
			if complete != 2 || incomplete != 1 {
				t.Fatalf("frames complete=%d incomplete=%d, want 2/1", complete, incomplete)
			}
			// Both transports receive every packet that was actually
			// sent (8); seq 6 is never sent and is charged as the single
			// end-of-stream gap via the scenario's declared tail.
			if res.PacketsSeen() != 8 {
				t.Fatalf("packets seen = %d, want 8", res.PacketsSeen())
			}
			// The incomplete frame must hold only its own timestamp's
			// single packet (seq 5); later-timestamp data is never merged.
			for _, f := range res.Frames() {
				if !f.Complete && f.Timestamp == 3000 && f.PacketCount != 1 {
					t.Fatalf("incomplete frame packet count wrong: %+v", f)
				}
			}
		})
	}
}

func TestWraparoundScenarioBothTransports(t *testing.T) {
	for _, mode := range []transport.Mode{transport.ModeUDP, transport.ModeInterleaved} {
		t.Run(string(mode), func(t *testing.T) {
			res := runScenario(t, testdata.ScenarioWraparound, mode)
			if res.Failure() != nil {
				t.Fatalf("session failed: %v", res.Failure())
			}
			complete, incomplete := res.FrameStats()
			if complete != 1 || incomplete != 0 {
				t.Fatalf("frames complete=%d incomplete=%d, want 1/0", complete, incomplete)
			}
			if res.GapCount() != 0 {
				t.Fatalf("wraparound produced false gap: %v", res.Gaps())
			}
		})
	}
}

func TestFailureInjectionReportsPartialStatistics(t *testing.T) {
	scenario, err := testdata.New(testdata.ScenarioGenerated)
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		"describe": "DESCRIBE",
		"setup":    "SETUP",
		"play":     "PLAY",
		"teardown": "TEARDOWN",
	}
	for name, method := range cases {
		t.Run(name, func(t *testing.T) {
			srv, err := sim.New(sim.Config{
				Packets:        scenario.Packets,
				PacketInterval: 2 * time.Millisecond,
				FailMethod:     method,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := srv.Start(); err != nil {
				t.Fatal(err)
			}
			defer srv.Close()

			res, err := transport.RunInterleaved(transport.InterleavedConfig{
				ServerURL:       srv.URL(),
				ExpectedPackets: len(scenario.Packets),
				RTPTimeout:      2 * time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			if res.Failure() == nil {
				t.Fatal("expected session failure, got success")
			}
			if res.FailureStage() == "" {
				t.Fatal("failure stage was not retained")
			}
			// Partial observations must still be present.
			if len(res.Transitions()) == 0 {
				t.Fatal("expected at least one observed transition before failure")
			}
			if name == "teardown" && res.PacketsSeen() == 0 {
				t.Fatal("expected packet statistics before the mid-stream teardown failure")
			}
		})
	}
}

func TestRunnerRejectsNonLoopbackTarget(t *testing.T) {
	if _, err := transport.RunUDP(transport.UDPConfig{
		ServerURL:       "rtsp://8.8.8.8:8554/stream",
		ExpectedPackets: 1,
	}); err == nil {
		t.Fatal("non-loopback target must be rejected")
	}
}
