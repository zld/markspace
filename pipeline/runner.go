package pipeline

import (
	pionrtp "github.com/pion/rtp"

	"mediacore/audio"
	"mediacore/cache"
	"mediacore/ingest"
	"mediacore/media"
	"mediacore/rtp"
)

// FPS is the fixed frame rate of the pipeline's unified timeline.
const FPS = 30

// WireFunc transforms emitted packets before recovery (loss/reorder
// simulation lives here, outside the kernel's correctness path).
type WireFunc func(frame int, pkts []*pionrtp.Packet) []*pionrtp.Packet

// Config is one pipeline run's full input. All fields deterministic in
// seed must be identical across resume attempts of the same run.
type Config struct {
	SourceID     cache.SourceID
	Codec        ingest.Codec
	Video        []byte
	Index        *ingest.KeyframeIndex
	Audio        []float32
	AudioMeta    ingest.AudioMeta
	Color        []ingest.ColorMeta // one observation per GOP, in order
	TargetLayout audio.Layout
	TargetRate   int
	Wire         WireFunc // nil = lossless
	Store        *Store   // nil = no checkpoints
	Cache        *cache.Cache
	OutPath      string // output container path (mp4)
	CrashAfter   int    // simulate process death after N frames; 0 = never
}

// Result reports a completed (or crashed) run.
type Result struct {
	OutputDigest       string
	VideoFrames        int
	AudioOutFrames     int64
	AVSyncP95Ms        int64
	LossRecoveryPct    float64
	LatencyP95Ms       int64
	ColorSummary       string
	CacheSourceChecked bool
	ErrorCode          media.Code // classified input fault encountered, "" if none
	Recovered          bool       // fault was isolated and the run completed
	Crashed            bool       // stopped early via CrashAfter
	VideoStats         rtp.Stats
	AudioStats         rtp.Stats
	InPeak, OutPeak    float32
	LastGain           float32
}

// Runner executes Config deterministically. Output bytes are a pure
// function of the config, so a crashed run resumed from its last
// checkpoint produces the same digest as an uninterrupted run.
type Runner struct {
	cfg Config
}

// NewRunner validates the static parts of cfg.
func NewRunner(cfg Config) (*Runner, error) {
	if cfg.TargetRate <= 0 {
		return nil, media.Fault(media.CodeInternal, "pipeline.NewRunner", -1, "target rate unset")
	}
	if cfg.Cache == nil {
		cfg.Cache = cache.New()
	}
	return &Runner{cfg: cfg}, nil
}

// Run executes the pipeline, resuming from the store's latest valid
// checkpoint when present. Implemented in runner_run.go.
func (r *Runner) Run() (Result, error) {
	return r.run()
}
