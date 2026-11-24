package pipeline

// Scenario summarises one transport pass.
type Scenario struct {
	Mode                string      `json:"mode"`
	LayerCount          int         `json:"layer_count"`
	ActiveLayers        int         `json:"active_layers"`
	TargetBitrateKbps   int         `json:"target_bitrate_kbps"`
	ActualBitrateKbps   float64     `json:"actual_bitrate_kbps"`
	BitrateDeviationPct float64     `json:"bitrate_deviation_pct"`
	QualityPSNRDb       float64     `json:"quality_psnr_db"`
	Duplicates          int         `json:"duplicates"`
	Reordered           int         `json:"reordered"`
	Lost                int         `json:"lost"`
	Gaps                int         `json:"gaps"`
	ManifestAlignNS     int64       `json:"manifest_alignment_ns"`
	RecoveredLayers     []string    `json:"recovered_layers"`
	IsolatedLayers      []string    `json:"isolated_layers"`
	Layers              []LayerStat `json:"layers"`
	Log                 []string    `json:"recovery_log"`
}
