package medpipe

// TrackSpec declares how one capture file is processed.
type TrackSpec struct {
	File      string `json:"file"`
	Track     string `json:"track"`
	Codec     Codec  `json:"codec"`
	Kind      Kind   `json:"kind"`
	ClockRate uint32 `json:"clock_rate"`
	Delta     uint64 `json:"rtp_delta"`
	// Audio target, only for KindAudio; nil skips audio validation.
	Audio *AudioConfig `json:"audio,omitempty"`
	// FatalExpected marks a fixture track known to be unrepairable; the
	// batch still reports it but skips execution after the scan fails.
	FatalExpected bool `json:"fatal_expected,omitempty"`
}

// PipelineOptions controls a batch run.
type PipelineOptions struct {
	InputDir  string
	WorkDir   string
	ChunkSize int
	TargetBPS float64
	Resume    bool
}
