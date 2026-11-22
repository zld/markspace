package sim

// Report is the printable, machine-parseable acceptance result. The JSON tags
// match the exact field names required by the verification command.
type Report struct {
	SyncErrorMSP95       float64        `json:"sync_error_ms_p95"`
	PacketRecoveryRate   float64        `json:"packet_recovery_rate"`
	PipelineLatencyMSP95 float64        `json:"pipeline_latency_ms_p95"`
	DriftPPMEstimate     float64        `json:"drift_ppm_estimate"`
	AudioSamples         int64          `json:"audio_samples"`
	EventSHA256          string         `json:"event_sha256"`
	RecoveryCount        int64          `json:"recovery_count"`
	Status               string         `json:"status"`
	Paths                map[string]int `json:"paths"`
}

const (
	statusOK            = "ok"
	statusRejected      = "rejected"
	statusMetricsFailed = "metrics_failed"
	statusCheckpointBad = "checkpoint_mismatch"
	reorderWindow       = 64
)
