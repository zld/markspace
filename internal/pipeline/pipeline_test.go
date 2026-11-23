package pipeline

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	fx "mediakernel/internal/fixture"
	"mediakernel/internal/mediaerr"
)

func baseCfg(t *testing.T) Config {
	t.Helper()
	return Config{
		Workers: 4, Queue: 16, ReorderWindow: 16,
		LossBurst: 8, BandwidthKbps: []int{900, 2500, 700},
		Seed: 42, OutputPath: filepath.Join(t.TempDir(), "out.mp4"),
	}
}

func TestRunNormalMeetsAcceptanceMetrics(t *testing.T) {
	cfg := baseCfg(t)
	fixture := fx.NewGenerator(cfg.Seed).Build(cfg.LossBurst, cfg.BandwidthKbps)
	man, code, err := Run(context.Background(), cfg, fixture)
	if err != nil || code != ExitOK {
		t.Fatalf("Run code=%d err=%v errors=%v", code, err, man.Errors)
	}
	m := man.Metrics
	if m.AVSyncErrorMillis > 20 {
		t.Fatalf("av_sync_error_ms=%d > 20", m.AVSyncErrorMillis)
	}
	if m.PacketRecoveryRate < 0.90 {
		t.Fatalf("packet_recovery_rate=%f < 0.90", m.PacketRecoveryRate)
	}
	if m.P95LatencyMillis > 500 {
		t.Fatalf("p95_latency_ms=%d > 500", m.P95LatencyMillis)
	}
	if m.ManifestSHA256 == "" {
		t.Fatal("manifest_sha256 missing")
	}
	if man.Remux == nil || !man.Remux.Verified {
		t.Fatal("remux summary missing or unverified")
	}
}

func TestRunIsDeterministicAcrossReruns(t *testing.T) {
	build := func(t *testing.T, out string) *Manifest {
		cfg := baseCfg(t)
		cfg.OutputPath = filepath.Join(out, "out.mp4")
		fixture := fx.NewGenerator(cfg.Seed).Build(cfg.LossBurst, cfg.BandwidthKbps)
		man, code, err := Run(context.Background(), cfg, fixture)
		if err != nil || code != ExitOK {
			t.Fatalf("Run code=%d err=%v", code, err)
		}
		return man
	}
	m1 := build(t, t.TempDir())
	m2 := build(t, t.TempDir())
	if m1.Metrics.ManifestSHA256 != m2.Metrics.ManifestSHA256 {
		t.Fatalf("rerun hash differs:\n%s\n%s", m1.Metrics.ManifestSHA256, m2.Metrics.ManifestSHA256)
	}
	if m1.Remux.SHA256 != m2.Remux.SHA256 {
		t.Fatal("remux media hash differs across reruns")
	}
}

func TestDamagedInputReturnsReportedCode(t *testing.T) {
	cfg := baseCfg(t)
	fixture := fx.NewGenerator(cfg.Seed).Build(cfg.LossBurst, cfg.BandwidthKbps)
	fixture.DASHMPD = fx.NewGenerator(cfg.Seed).DamagedDASH()
	man, code, err := Run(context.Background(), cfg, fixture)
	if err != nil {
		t.Fatalf("reported damage must not be a Go error: %v", err)
	}
	if code != ExitReported {
		t.Fatalf("code=%d, want %d", code, ExitReported)
	}
	if len(man.Errors) == 0 {
		t.Fatal("damaged run must record structured errors")
	}
}

func TestInvalidConfigReturnsFailureCode(t *testing.T) {
	cfg := baseCfg(t)
	cfg.Workers = 0
	fixture := fx.NewGenerator(42).Build(8, nil)
	_, code, err := Run(context.Background(), cfg, fixture)
	if err == nil || code != ExitFailure {
		t.Fatalf("got code=%d err=%v, want failure", code, err)
	}
	if me, ok := mediaerr.As(err); !ok || me.Category != mediaerr.CatValidation {
		t.Fatalf("err=%v, want validation error", err)
	}
}

func TestCancellationPropagatesAndExits(t *testing.T) {
	cfg := baseCfg(t)
	cfg.Workers, cfg.Queue = 2, 1
	fixture := fx.NewGenerator(cfg.Seed).Build(cfg.LossBurst, cfg.BandwidthKbps)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel so every stage observes it immediately

	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		_, c, e := Run(ctx, cfg, fixture)
		done <- result{c, e}
	}()
	select {
	case r := <-done:
		if r.code != ExitFailure {
			t.Fatalf("canceled run code=%d, want %d", r.code, ExitFailure)
		}
		if r.err == nil {
			t.Fatal("canceled run must return a cancellation error")
		}
		if me, ok := mediaerr.As(r.err); !ok || me.Category != mediaerr.CatCanceled {
			t.Fatalf("err=%v, want canceled category", r.err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled pipeline did not exit within 1s")
	}
}
