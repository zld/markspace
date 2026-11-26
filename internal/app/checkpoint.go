// Package app wires the rtp, ts, audio, pipeline and manifest packages into
// one deterministic regression run, including checkpointed cancellation and
// resume. All heavy logic lives here so cmd/regress stays a thin flag wrapper.
package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"rtpregress/internal/clock"
)

// CheckpointFileName is the on-disk resume state file inside the output dir.
const CheckpointFileName = "checkpoint.json"

// CompletedSegment records one fully processed segment for resume skipping.
type CompletedSegment struct {
	SegNumber   int    `json:"segment"`
	PID         uint16 `json:"pid"`
	StartNanos  int64  `json:"start_nanos"`
	StartSample int64  `json:"start_sample"`
	Samples     int    `json:"samples"`
	PCMSHA256   string `json:"pcm_sha256"`
	PCMFile     string `json:"pcm_file"`
	// acoustic evidence, persisted so a resumed summary matches a fresh run
	MeanSquare float64 `json:"mean_square"`
	TruePeak   float64 `json:"true_peak_dbtp"`
	Sinad      float64 `json:"sinad_db"`
}

// Checkpoint is the durable, content-addressed run state.
type Checkpoint struct {
	Schema      string             `json:"schema"`
	InputSHA256 string             `json:"input_sha256"`
	WorkerCount int                `json:"worker_count"`
	TargetRate  int                `json:"target_rate"`
	Channels    int                `json:"channels"`
	Bitrate     int                `json:"bitrate"`
	Completed   []CompletedSegment `json:"completed"`
	RunHash     string             `json:"run_hash"`
}

// CheckpointPath joins the output directory with the checkpoint file name.
func CheckpointPath(outDir string) string {
	return filepath.Join(outDir, CheckpointFileName)
}

// LoadCheckpoint reads and validates a checkpoint. A missing file returns
// (nil, nil); a corrupt or mismatched-input file returns a stable error.
func LoadCheckpoint(path, inputSHA string) (*Checkpoint, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, clock.New(clock.CodeCheckpoint, "app", -1, 0, 0,
			"read checkpoint: %v", err)
	}
	var cp Checkpoint
	if jerr := json.Unmarshal(data, &cp); jerr != nil {
		return nil, clock.New(clock.CodeCheckpoint, "app", -1, 0, 0,
			"corrupt checkpoint json: %v", jerr)
	}
	if cp.InputSHA256 != inputSHA {
		return nil, clock.New(clock.CodeCheckpoint, "app", -1, 0, 0,
			"checkpoint is for a different input; refusing to resume")
	}
	return &cp, nil
}

// Save atomically writes the checkpoint (temp file + rename) so a crash never
// leaves a half-written state file.
func (c *Checkpoint) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return clock.New(clock.CodeCheckpoint, "app", -1, 0, 0,
			"checkpoint mkdir: %v", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return clock.New(clock.CodeCheckpoint, "app", -1, 0, 0,
			"checkpoint marshal: %v", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return clock.New(clock.CodeCheckpoint, "app", -1, 0, 0,
			"checkpoint write: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return clock.New(clock.CodeCheckpoint, "app", -1, 0, 0,
			"checkpoint rename: %v", err)
	}
	return nil
}

// HasSegment reports whether a segment number is already complete.
func (c *Checkpoint) HasSegment(segNum int) bool {
	for _, s := range c.Completed {
		if s.SegNumber == segNum {
			return true
		}
	}
	return false
}

// FindSegment returns the completed row for a segment number.
func (c *Checkpoint) FindSegment(segNum int) (CompletedSegment, bool) {
	for _, s := range c.Completed {
		if s.SegNumber == segNum {
			return s, true
		}
	}
	return CompletedSegment{}, false
}

// ComputeRunHash hashes the completed-segment coordinates and PCM digests in a
// canonical order (segment number), so the value is independent of the order in
// which workers happened to finish. A resumed run therefore matches a fresh
// run with a different worker count.
func ComputeRunHash(completed []CompletedSegment) string {
	ordered := make([]CompletedSegment, len(completed))
	copy(ordered, completed)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].SegNumber < ordered[j].SegNumber
	})
	h := sha256.New()
	for _, s := range ordered {
		var b [8]byte
		put64(b[:], uint64(s.StartSample))
		h.Write(b[:])
		put64(b[:], uint64(s.PID))
		h.Write(b[:])
		put64(b[:], uint64(s.SegNumber))
		h.Write(b[:])
		h.Write([]byte(s.PCMSHA256))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func put64(b []byte, v uint64) {
	for i := 7; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
}
