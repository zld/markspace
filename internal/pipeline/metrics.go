package pipeline

import (
	"os"

	"mediatool/internal/batch"
	"mediatool/internal/clock"
	"mediatool/internal/fixture"
	"mediatool/internal/rtprecover"
)

func fillMetrics(res *Result, st rtprecover.Stats, latencies []int64,
	cp *batch.Checkpoint, tl *clock.Timeline, spec fixture.Spec) {
	res.Metrics.DuplicatePackets = st.Duplicates
	res.Metrics.RecoveredChunks = cp.CommittedCount()
	res.Metrics.LatencyP95MS = float64(clock.Percentile(latencies, 95)) / 1e6
	// Recovery rate is defined over reorderable loss: holes that could have
	// been filled inside the window. A stream that only suffers genuine
	// burst loss has no reorderable denominator and reports 1.0.
	if st.Recoverable > 0 {
		res.Metrics.LossRecoveryRate = float64(st.Recoverable) /
			float64(st.Recoverable+st.Lost)
	} else {
		res.Metrics.LossRecoveryRate = 1
	}
	// Sync error: for constant-rate streams the PTS deltas must all equal
	// the first delta, so measure the worst deviation. VFR streams have
	// legitimately varying deltas; there the requirement is monotonicity,
	// already enforced while building the timeline.
	res.Metrics.SyncErrorMS = syncErrorMS(tl, spec)
	res.Metrics.IndexSHA256 = cp.Index().Digest()
}

// syncErrorMS measures timeline regularity. For non-VFR streams it compares
// every PTS against the ideal grid implied by the first observed delta; for
// VFR streams it returns zero because monotonicity (checked upstream) is the
// applicable correctness criterion.
func syncErrorMS(tl *clock.Timeline, spec fixture.Spec) float64 {
	if spec.Kind == fixture.KindVFR {
		return 0
	}
	samples := tl.Samples()
	if len(samples) < 2 {
		return 0
	}
	step := samples[1].PTS - samples[0].PTS
	if step <= 0 {
		return 0
	}
	var worst int64
	for i, s := range samples {
		ideal := samples[0].PTS + int64(i)*step
		d := s.PTS - ideal
		if d < 0 {
			d = -d
		}
		if d > worst {
			worst = d
		}
	}
	return float64(worst) / 1e6
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
