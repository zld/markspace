package quality

import (
	"math"
	"math/rand"
	"testing"

	"archive-lab/internal/media"
)

// synthFrames builds deterministic 64x48 8-bit luma frames with a smooth
// gradient plus noise, similar in statistics to a real video.
func synthFrames(n int, seed int64) [][]byte {
	rng := rand.New(rand.NewSource(seed))
	const w, h = 64, 48
	frames := make([][]byte, n)
	for f := 0; f < n; f++ {
		buf := make([]byte, w*h)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				v := (x*3 + y*2 + f*7) % 256
				v += rng.Intn(9) - 4
				buf[y*w+x] = byte(clamp8(v))
			}
		}
		frames[f] = buf
	}
	return frames
}

func TestCodecRoundTripsAndMeasuresRealError(t *testing.T) {
	frames := synthFrames(3, 7)
	for _, q := range []int{100, 400, 1200, 4000} {
		for i, f := range frames {
			payload, st, err := EncodeFrame(f, 64, 48, q)
			if err != nil {
				t.Fatal(err)
			}
			got, err := DecodeFrame(payload, 64, 48)
			if err != nil {
				t.Fatal(err)
			}
			if len(payload) != st.Bytes {
				t.Fatalf("payload %d != stats %d", len(payload), st.Bytes)
			}
			var sse int64
			for j := range got {
				d := int64(got[j]) - int64(f[j])
				sse += d * d
			}
			if sse != st.SSE {
				t.Fatalf("frame %d q=%d: decoded SSE %d != measured %d", i, q, sse, st.SSE)
			}
		}
	}
}

func TestQuantiserMonotonicity(t *testing.T) {
	frames := synthFrames(1, 3)
	prevBytes := math.MaxInt
	var prevPSNR float64 = -1
	for _, q := range []int{100, 200, 400, 800, 1600} {
		payload, st, err := EncodeFrame(frames[0], 64, 48, q)
		if err != nil {
			t.Fatal(err)
		}
		psnr, _ := PSNRFromSSE(st.SSE, st.Pixels)
		if len(payload) > prevBytes {
			t.Fatalf("q=%d produced %d bytes, more than previous %d", q, len(payload), prevBytes)
		}
		// q=1.00 is lossless for these synthetic planes; after that PSNR
		// must be non-increasing as the quantiser coarsens.
		if prevPSNR >= 0 && psnr > prevPSNR+1e-9 {
			t.Fatalf("q=%d PSNR %.2f rose above previous %.2f", q, psnr, prevPSNR)
		}
		prevBytes, prevPSNR = len(payload), psnr
	}
}

func TestRateControlMeetsTargetAndQuality(t *testing.T) {
	frames := synthFrames(10, 11)
	spec := VideoSpec{Width: 64, Height: 48, FPSNum: 30, FPSDen: 1}
	req := Request{TargetBps: 800_000, MaxBps: 900_000, MinPSNR: 35, BudgetMs: 5000}
	plan, err := EstimatePlan(req, Sample{Spec: spec, Frames: frames}, 3, 3_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.PSNRKnown {
		t.Fatal("PSNR should be measurable for uncompressed luma")
	}
	if plan.EstPSNR < 35 {
		t.Fatalf("estimated PSNR %.2f below request", plan.EstPSNR)
	}
	if plan.Config.Digest() == "" {
		t.Fatal("config digest must be non-empty")
	}
	if plan.Config.Digest() == (Config{Codec: CodecVersion, TargetBps: req.TargetBps, MaxBps: req.MaxBps, QuantizerMilli: plan.Config.QuantizerMilli + 1}).Digest() {
		t.Fatal("digest must depend on the quantiser")
	}
	if !plan.Feasible {
		t.Fatalf("expected feasible plan, unmet=%v reason=%s", plan.Unmet, plan.Reason)
	}
}

func TestConflictingConstraintsAreReported(t *testing.T) {
	spec := VideoSpec{Width: 64, Height: 48, FPSNum: 30, FPSDen: 1}
	frames := synthFrames(4, 5)
	// An impossibly high quality floor must surface as infeasible, never be
	// rounded into a pass.
	req := Request{TargetBps: 2_000, MaxBps: 2_000, MinPSNR: 99, BudgetMs: 50}
	plan, err := EstimatePlan(req, Sample{Spec: spec, Frames: frames}, 2, 3_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Feasible || plan.Reason == "" || len(plan.Unmet) == 0 {
		t.Fatalf("expected infeasible plan, got %+v", plan)
	}
	if !plan.Infeasible {
		t.Fatal("a genuine constraint contradiction must be flagged infeasible")
	}
	if err := (Request{TargetBps: 900_000, MaxBps: 800_000, MinPSNR: 35, BudgetMs: 10}).Validate(); err == nil {
		t.Fatal("target above cap must be rejected")
	}
	if code := media.ExitCode(media.Usage("bad flag")); code != 1 {
		t.Fatalf("usage errors exit 1, got %d", code)
	}
}

func TestUnmeasurableSampleIsExplicit(t *testing.T) {
	req := Request{TargetBps: 800_000, MaxBps: 900_000, MinPSNR: 35, BudgetMs: 5000}
	plan, err := EstimatePlan(req, Sample{}, 2, 3_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if plan.PSNRKnown || plan.Unmet[0] != "psnr_unmeasurable" {
		t.Fatalf("unmeasurable sample must be explicit, got %+v", plan)
	}
}
