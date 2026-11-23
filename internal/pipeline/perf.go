package pipeline

import "time"

// BenchmarkSegments runs the full pipeline over n independently generated
// fixture segments and returns the P95 wall-clock duration in milliseconds.
// Each segment uses a derived seed so the workload is deterministic.
func BenchmarkSegments(baseSeed int64, lossWindow, n int) (p95Ms float64, err error) {
	durations := make([]float64, 0, n)
	for i := 0; i < n; i++ {
		set := newSegmentSet(baseSeed, int64(i), lossWindow)
		start := time.Now()
		if _, rerr := Run(set, Options{
			Seed:       baseSeed + int64(i),
			LossWindow: lossWindow,
			Render:     smallRenderOptions(),
		}); rerr != nil {
			return 0, rerr
		}
		durations = append(durations, float64(time.Since(start).Microseconds())/1000.0)
	}
	return percentile(durations, 95), nil
}

// AttachPerformance stamps the per-run P95 pipeline latency onto res by
// measuring the supplied count of generated segments.
func (res *Result) AttachPerformance(baseSeed int64, lossWindow, count int) error {
	p95, err := BenchmarkSegments(baseSeed, lossWindow, count)
	if err != nil {
		return err
	}
	res.PipelineP95Ms = p95
	return nil
}
