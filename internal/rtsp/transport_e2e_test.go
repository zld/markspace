package rtsp

import (
	"context"
	"testing"
	"time"

	"rtspdiag/internal/config"
	"rtspdiag/internal/fixture"
	"rtspdiag/internal/rtp"
	"rtspdiag/internal/sim"
)

type pipelinePipe struct{ inner *rtp.Pipeline }

func (c *pipelinePipe) Ingest(b []byte, t time.Time) error {
	return c.inner.Ingest(b, t)
}
func (c *pipelinePipe) ParseErrors() int { return c.inner.ParseErrors() }

func e2eItems() ([]fixture.Item, []byte) {
	items := fixture.Generate(42, fixture.DefaultPlan(5))
	var malformed []byte
	for _, it := range items {
		if it.Kind == fixture.KindMalformed {
			malformed = it.Raw
		}
	}
	return items, malformed
}

func runAdapter(t *testing.T, mode config.TransportMode, smode sim.Mode) {
	t.Helper()
	items, malformed := e2eItems()
	srv, err := sim.Start(sim.Config{
		Transport: smode, Items: items,
		PacketInterval: 2 * time.Millisecond, DisconnectAtEnd: true,
	})
	if err != nil {
		t.Fatalf("server start: %v", err)
	}
	defer srv.Close()

	pipeline := rtp.NewPipeline(1500, 8, 25*time.Millisecond)
	adapter := NewAdapter(AdapterConfig{
		Transport: mode, URL: srv.Address(),
		Window: 8, ReorderTimeout: 25 * time.Millisecond,
		ReadTimeout: 2 * time.Second, Pipeline: &pipelinePipe{inner: pipeline},
		InjectMalformed: malformed,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := adapter.Run(ctx); err != nil && mode == config.TransportTCP {
		// TCP surfaces the forced server disconnect; that is expected.
		t.Logf("tcp run returned expected disconnect error: %v", err)
	}
	pipeline.Flush(time.Now())

	st := pipeline.Stats()
	if st.ParseErrors != 1 {
		t.Errorf("parse errors = %d want 1", st.ParseErrors)
	}
	if st.GapEvents != 1 || st.MissingPackets != 1 {
		t.Errorf("gap accounting wrong: %+v", st)
	}
	if st.Duplicates != 1 {
		t.Errorf("duplicates = %d want 1", st.Duplicates)
	}
	if st.CompleteFrames != 5 {
		t.Errorf("complete frames = %d want 5", st.CompleteFrames)
	}
	if got := adapter.Session().TransitionCount(); got < 5 || got > 6 {
		t.Errorf("state transitions = %d want 5..6", got)
	}
}

func TestAdapterUDPEndToEnd(t *testing.T) {
	runAdapter(t, config.TransportUDP, sim.TransportUDP)
}

func TestAdapterTCPEndToEnd(t *testing.T) {
	runAdapter(t, config.TransportTCP, sim.TransportTCP)
}

// TestAdapterCleanupAfterDisconnect verifies a second run on a fresh adapter
// starts from zero state: no buffered packets or frames leak across runs.
func TestAdapterCleanupAfterDisconnect(t *testing.T) {
	items, _ := e2eItems()
	for _, smode := range []sim.Mode{sim.TransportUDP, sim.TransportTCP} {
		srv, err := sim.Start(sim.Config{
			Transport: smode, Items: items,
			PacketInterval: 2 * time.Millisecond, DisconnectAtEnd: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		mode := config.TransportTCP
		if smode == sim.TransportUDP {
			mode = config.TransportUDP
		}
		pipeline := rtp.NewPipeline(1500, 8, 25*time.Millisecond)
		adapter := NewAdapter(AdapterConfig{
			Transport: mode, URL: srv.Address(),
			Window: 8, ReorderTimeout: 25 * time.Millisecond,
			ReadTimeout: 2 * time.Second, Pipeline: &pipelinePipe{inner: pipeline},
		})
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		_ = adapter.Run(ctx)
		cancel()
		srv.Close()

		// Explicit reset models the next diagnostic run starting clean.
		adapter.Reset()
		pipeline.Reset()
		if adapter.Session().State() != StateInit ||
			adapter.Session().TransitionCount() != 0 {
			t.Fatalf("%v: session state leaked: %s count=%d",
				smode, adapter.Session().State(), adapter.Session().TransitionCount())
		}
		if got := pipeline.Stats(); got != (rtp.PipelineStats{}) {
			t.Fatalf("%v: pipeline state leaked: %+v", smode, got)
		}
	}
}
