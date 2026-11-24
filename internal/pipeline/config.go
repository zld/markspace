package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"time"

	"mediarc/internal/model"
	"mediarc/internal/normalize"
)

// Options are the deterministic batch processing parameters.
type Options struct {
	Workdir         string   `json:"workdir"`
	Seed            int64    `json:"seed"`
	Frames          int      `json:"frames"`
	AudioPackets    int      `json:"audio_packets"`
	ChunkSize       int      `json:"chunk_size"`
	Inject          []string `json:"inject"`
	InterruptAfter  int      `json:"interrupt_after"`
	TargetRate      uint32   `json:"target_rate"`
	SourceLayout    string   `json:"source_layout"`
	TargetLayout    string   `json:"target_layout"`
	AVStartOffsetMS int64    `json:"av_start_offset_ms"`
	Window          int      `json:"window"`
	LateThreshold   int      `json:"late_threshold"`
}

// ConfigPath returns the persisted options path.
func ConfigPath(workdir string) string { return filepath.Join(workdir, "config.json") }

// DefaultOptions fills the acceptance defaults.
func DefaultOptions() Options {
	return Options{
		Seed: 17, Frames: 120, AudioPackets: 480, ChunkSize: 16,
		TargetRate: 48000, SourceLayout: "stereo", TargetLayout: "stereo",
		Window: 64, LateThreshold: 8,
	}
}

// HasInject reports whether a fault token was requested.
func (o Options) HasInject(name string) bool {
	for _, x := range o.Inject {
		if x == name {
			return true
		}
	}
	return false
}

// AVOffset converts the CLI millisecond offset to duration.
func (o Options) AVOffset() time.Duration { return time.Duration(o.AVStartOffsetMS) * time.Millisecond }

// Layouts resolves declared channel layouts. Unknown layouts return exit 3.
func (o Options) Layouts() (normalize.Layout, normalize.Layout, error) {
	srcName := orDefault(o.SourceLayout, "stereo")
	dstName := orDefault(o.TargetLayout, "stereo")
	src, ok := normalize.LookupLayout(srcName)
	if !ok {
		return normalize.Layout{}, normalize.Layout{}, model.NewExitError(model.ExitSyncConfig,
			"unknown source channel layout: "+srcName,
			model.Fault{Kind: model.FaultUnknownChannel, Actual: srcName})
	}
	dst, ok := normalize.LookupLayout(dstName)
	if !ok {
		return normalize.Layout{}, normalize.Layout{}, model.NewExitError(model.ExitSyncConfig,
			"unknown target channel layout: "+dstName,
			model.Fault{Kind: model.FaultUnknownChannel, Actual: dstName})
	}
	return src, dst, nil
}

// ParamsHash hashes all parameters that affect output bytes. Interruption and
// workdir are intentionally excluded.
func (o Options) ParamsHash() string {
	o.Workdir = ""
	o.InterruptAfter = 0
	b, _ := json.Marshal(o)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}
