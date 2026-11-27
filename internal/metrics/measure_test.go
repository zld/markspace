package metrics_test

import (
	"testing"

	"mediasync/internal/fixture"
	"mediasync/internal/index"
	"mediasync/internal/model"
	"mediasync/internal/metrics"
	"mediasync/internal/rtp"
	"mediasync/internal/sync"
)

func TestMeasureLatencyP95(t *testing.T) {
	// Latencies 1..20 ms; P95 nearest rank = ceil(20*0.95)=19 -> 19 ms.
	pkts := make([]rtp.Packet, 20)
	for i := range pkts {
		lat := model.Ticks(i+1) * model.Milli
		pkts[i] = rtp.Packet{
			ExtSequence: uint64(i),
			ArrivalNS:   lat, // schedule at 0
			ScheduleNS:  0,
		}
	}
	got := metrics.MeasureLatency(pkts)
	if got.P95NS != 19*model.Milli {
		t.Fatalf("P95 = %v, want 19ms", got.P95Millis)
	}
	if got.MaxNS != 20*model.Milli || got.MinNS != model.Milli {
		t.Fatalf("min/max wrong: %+v", got)
	}
	if got.P95Millis > 100 {
		t.Fatal("acceptance budget is P95 <= 100 ms")
	}
}

func TestMeasureLossNoLoss(t *testing.T) {
	st := rtp.Stats{Committed: 10, Missing: 0, Duplicates: 0}
	got := metrics.MeasureLoss(st, 0)
	if got.RecoveryRate != 1.0 || got.Unrecovered != 0 {
		t.Fatalf("clean stream recovery must be 1.0: %+v", got)
	}
}

func TestMeasureLossPartialRecovery(t *testing.T) {
	// 4 injected losses: 3 refilled inside the window, 1 given up.
	st := rtp.Stats{Missing: 1, Duplicates: 2, Late: 1}
	got := metrics.MeasureLoss(st, 4)
	if got.Recovered != 3 || got.Unrecovered != 1 {
		t.Fatalf("recovery split wrong: %+v", got)
	}
	if got.RecoveryRate != 0.75 {
		t.Fatalf("rate = %f, want 0.75", got.RecoveryRate)
	}
	if got.Duplicates != 2 || got.Late != 1 {
		t.Fatalf("duplicate/late counters lost: %+v", got)
	}
}

func TestMeasureSyncFromReport(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/m.mp4"
	specs := []fixture.TrackSpec{
		{Handler: "soun", TrackID: 1, Timescale: 1000, Deltas: []uint32{100, 100, 100}},
		{Handler: "subt", TrackID: 2, Timescale: 1000, Deltas: []uint32{100, 100, 100}},
	}
	if _, err := fixture.WriteMP4(path, 1000, specs); err != nil {
		t.Fatal(err)
	}
	res, err := index.Scan(path, model.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	cues := sync.BuildCuesWithJitter(res.FindTrack("subt"),
		[]model.Ticks{model.Milli / 2, model.Milli / 2, model.Milli / 2})
	rep, err := sync.Align(res.FindTrack("soun"), cues)
	if err != nil {
		t.Fatal(err)
	}
	got := metrics.MeasureSync(rep, model.Milli)
	if !got.WithinLimit || got.MaxErrorNS > model.Milli {
		t.Fatalf("sub-millisecond error must pass the 1 ms budget: %+v", got)
	}
	over := metrics.MeasureSync(rep, model.Milli/4)
	if over.WithinLimit {
		t.Fatal("error above threshold must fail the budget")
	}
}
