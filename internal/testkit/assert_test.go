package testkit

import (
	"testing"
	"time"

	"jbbench/internal/metrics"
	"jbbench/internal/sim"
)

func validReport() metrics.Report {
	c := metrics.New(100)
	c.SetFrames(100)
	for i := 0; i < 100; i++ {
		c.AddReceived()
		c.RecordFrame(time.Duration(i)*20*time.Millisecond, 3,
			5*time.Millisecond, i%10 == 0)
	}
	c.SetConcealment(3, 100, 4, 3)
	return c.Build()
}

func TestAssertReportFieldsAcceptsValid(t *testing.T) {
	// A failing assertion inside the helpers calls t.Error on this test, so a
	// clean run proves the valid report passes every check.
	AssertReportFields(t, validReport())
}

func TestAssertReportFieldsRejectsBadRatio(t *testing.T) {
	r := validReport()
	r.RecoveryRatio = 1.5
	if err := metrics.Validate(r); err == nil {
		t.Fatal("invalid recovery ratio was not flagged by the shared check")
	}
	r = validReport()
	r.Played = r.Frames + 1
	if err := metrics.Validate(r); err == nil {
		t.Fatal("played > frames was not flagged by the shared check")
	}
}

func TestScenarioAccounting(t *testing.T) {
	cfg := sim.DefaultConfig()
	cfg.Frames = 300
	events, err := sim.Generate(cfg)
	Require(t, err, "generate")
	AssertScenarioAccounting(t, events, cfg.Frames)
	AssertClassified(t, events, sim.EventOK, sim.EventAmbiguous)
}
