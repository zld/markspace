package pipeline

import (
	"medkernel/media"
	"medkernel/txn"
)

// Thresholds bounds the end-to-end acceptance checks.
type Thresholds struct {
	SyncErrorMaxMS float64
	P95LatencyMax  float64
	LUFSTarget     float64
}

// DefaultThresholds matches the acceptance criteria in the project brief.
func DefaultThresholds() Thresholds {
	return Thresholds{SyncErrorMaxMS: 20, P95LatencyMax: 150, LUFSTarget: -23}
}

// ptsStep90 is the fixture frame increment on the 90 kHz axis (40 ms).
const ptsStep90 uint64 = 3600

// Metrics is the final JSON-serializable report.
type Metrics struct {
	Scenario         string             `json:"scenario"`
	SyncErrorMS      float64            `json:"sync_error_ms"`
	RecoveredPackets int64              `json:"recovered_packets"`
	LossPackets      int64              `json:"loss_packets"`
	P95LatencyMS     float64            `json:"p95_latency_ms"`
	AudioLUFS        float64            `json:"audio_lufs"`
	RollbackCount    int                `json:"rollback_count"`
	CommitHash       string             `json:"commit_hash"`
	RecoveryRatePct  float64            `json:"recovery_rate_pct"`
	FaultRanges      []FaultRange       `json:"fault_ranges,omitempty"`
	StageMetrics     map[string]float64 `json:"stage_metrics"`
}

// FaultRange reports a faulty packet's PID, index and source byte range.
type FaultRange struct {
	Class   string `json:"class"`
	PID     uint16 `json:"pid"`
	Index   int64  `json:"index"`
	ByteOff int64  `json:"byte_off"`
	ByteEnd int64  `json:"byte_end"`
}

// Runner executes one scenario through the journaled stage pipeline.
type Runner struct {
	dir        string
	thresholds Thresholds
}

// NewRunner builds a runner writing journal/artifacts under dir.
func NewRunner(dir string, t Thresholds) *Runner { return &Runner{dir: dir, thresholds: t} }

// strictSLO reports whether the numeric service-level checks apply. Only the
// healthy reference stream is held to the thresholds; fault-injection
// scenarios run to completion so they can report their evidence instead.
func strictSLO(scenario string) bool { return scenario == ScenNormal }

// Run executes every stage for fx and returns the consolidated metrics.
// Stage validation failures roll that stage back; the returned error lets the
// caller decide the process exit code while diagnostics stay in the journal.
func (r *Runner) Run(scenario string, fx *Fixtures, rtpfx *Fixtures) (m Metrics, err error) {
	committer, err := txn.NewCommitter(r.dir)
	if err != nil {
		return Metrics{}, err
	}
	defer committer.Close()

	m = Metrics{Scenario: scenario, StageMetrics: map[string]float64{}}
	// Even when a validation stage rolls back and Run returns early, the
	// reported metrics carry the rollback count and the hash of whatever
	// committed boundary chain exists.
	defer func() {
		m.RollbackCount = committer.RollbackCount()
		m.CommitHash = committer.CommitHash()
	}()

	// Stage 1: structural TS parse with fault capture.
	parseSum, _, err := committer.Execute(txn.Stage{
		Name: "ts_parse", InputHash: hashBuffer(fx),
		Run: func(workdir string) (string, map[string]float64, error) {
			return r.stageParse(workdir, fx)
		},
	})
	if err != nil {
		return m, err
	}

	// Stage 2: clock/PCR/PTS monotonicity and PSI boundaries.
	_, _, err = committer.Execute(txn.Stage{
		Name: "ts_clock", InputHash: parseSum.OutputHash,
		Run: func(workdir string) (string, map[string]float64, error) {
			return r.stageClock(workdir, fx, &m)
		},
		Check: func(metrics map[string]float64) error {
			if metrics["clock_faults"] > 0 {
				return media.New(media.ClassValidation, "pipeline.clock",
					"clock backwards/drift faults detected")
			}
			return nil
		},
	})
	if err != nil {
		return m, err
	}

	// Stage 3: audio normalization on a synthesized track matching PSI meta.
	_, _, err = committer.Execute(txn.Stage{
		Name: "audio_norm", InputHash: parseSum.OutputHash,
		Run: func(workdir string) (string, map[string]float64, error) {
			return r.stageAudio(workdir, fx, &m)
		},
	})
	if err != nil {
		return m, err
	}

	// Stage 4: RTP reorder / recovery / loss accounting.
	_, _, err = committer.Execute(txn.Stage{
		Name: "rtp_jitter", InputHash: hashRTP(rtpfx),
		Run: func(workdir string) (string, map[string]float64, error) {
			return r.stageRTP(workdir, rtpfx, &m)
		},
		Check: func(metrics map[string]float64) error {
			if strictSLO(scenario) &&
				metrics["p95_latency_ms"] > r.thresholds.P95LatencyMax {
				return media.New(media.ClassValidation, "pipeline.rtp",
					"p95 latency exceeds threshold")
			}
			return nil
		},
	})
	if err != nil {
		return m, err
	}

	// Stage 5: end-to-end A/V sync error on the shared 90 kHz axis.
	_, _, err = committer.Execute(txn.Stage{
		Name: "av_sync", InputHash: parseSum.OutputHash,
		Run: func(workdir string) (string, map[string]float64, error) {
			return r.stageSync(workdir, fx, &m)
		},
		Check: func(metrics map[string]float64) error {
			if strictSLO(scenario) &&
				metrics["sync_error_ms"] > r.thresholds.SyncErrorMaxMS {
				return media.New(media.ClassValidation, "pipeline.sync",
					"A/V sync error exceeds threshold")
			}
			return nil
		},
	})
	if err != nil {
		return m, err
	}
	return m, nil
}
