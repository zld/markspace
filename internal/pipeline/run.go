// Package pipeline wires the container, clock, timeline, audio and checkpoint
// subsystems into one deterministic processing kernel. Stage boundaries
// enforce invariants (monotonic time, sample conservation, interval coverage),
// and the full restartable state is snapshotted atomically so a resumed run
// reproduces the exact output checksum of an uninterrupted one.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash"
	"os"

	"mediatimeline/internal/audio"
	"mediatimeline/internal/checkpoint"
	"mediatimeline/internal/clock"
	"mediatimeline/internal/timeline"
)

// Config parameterizes a run. All fields are plain values so the whole config
// is reproducible from the CLI and embedded in the checkpoint.
type Config struct {
	Seed            int64   `json:"seed"`
	LateMS          int     `json:"late_ms"`
	CheckpointEvery int     `json:"checkpoint_every"`
	InRate          uint32  `json:"in_rate"`
	OutRate         uint32  `json:"out_rate"`
	Channels        int     `json:"channels"`
	TargetDBFS      float64 `json:"target_dbfs"`
	Ceiling         float64 `json:"ceiling"`
	WindowFrames    int     `json:"window_frames"`
}

// Metrics is the deterministic run summary printed by the CLI.
type Metrics struct {
	EventCount      int64   `json:"event_count"`
	WatermarkNS     uint64  `json:"watermark_ns"`
	DriftPPM        float64 `json:"drift_ppm"`
	ResampleFrames  uint64  `json:"resample_frames"`
	PeakDBFS        float64 `json:"peak_dbfs"`
	OutputSHA256    string  `json:"output_sha256"`
	LateEvents      uint64  `json:"late_events"`
	ReplayedEvents  uint64  `json:"replayed_events"`
	SkippedDup      uint64  `json:"skipped_duplicates"`
	RecoverableErrs int     `json:"recoverable_errors"`
	Resumed         bool    `json:"resumed"`
}

// state is the complete restartable kernel state, serialized to checkpoints.
type state struct {
	Config         Config                  `json:"config"`
	Seq            uint64                  `json:"seq"`
	Unwrap         clock.Snapshot          `json:"unwrap"`
	Drift          clock.DriftSnapshot     `json:"drift"`
	Watermark      timeline.WMSnapshot     `json:"watermark"`
	Replay         timeline.ReplaySnapshot `json:"replay"`
	ResamplePhase  uint64                  `json:"resample_phase_num"`
	ResampleDen    uint64                  `json:"resample_phase_den"`
	ResampleCons   uint64                  `json:"resample_consumed"`
	ResampleFrames uint64                  `json:"resample_frames"`
	ResampleTotal  uint64                  `json:"resample_total_input"`
	PrevFrame      []float64               `json:"prev_frame"`
	Loudness       audio.LoudnessState     `json:"loudness"`
	NextSample     int64                   `json:"next_sample"`
	EventCount     int64                   `json:"event_count"`
	NodeOrigin     uint64                  `json:"node_origin"`
	NodeOriginSet  bool                    `json:"node_origin_set"`
	OutputBytes    uint64                  `json:"output_bytes"`
	TotalIn        uint64                  `json:"total_in"`
	TotalOut       uint64                  `json:"total_out"`
}

// Pipeline owns the live stage objects and their shared metrics.
type Pipeline struct {
	cfg           Config
	unwrap        *clock.Unwrapper
	drift         *clock.DriftEstimator
	wm            *timeline.Watermark
	replay        *timeline.ReplayLog
	resample      *audio.Resampler
	mapper        *audio.Mapper
	loud          *audio.Loudness
	store         *checkpoint.Store
	hash          hash.Hash
	outFile       string
	payloadReader interface {
		ReadAt([]byte, int64) (int, error)
	}
	recoverableErrs int
	pendingErr      error
	resumed         bool
	resumedEvents   uint64 // events newly processed during a resumed run
	st              state
	totalIn         uint64
	totalOut        uint64
	nodeOrigin      uint64 // first absolute node tick, drift-fit origin
	nodeOriginSet   bool
}

// New builds a pipeline rooted at a checkpoint directory. A valid checkpoint
// is restored and the output checksum is rebuilt from the durable PCM file;
// otherwise a fresh kernel is initialized.
func New(cfg Config, ckptDir, outputPath string) (*Pipeline, error) {
	store, err := checkpoint.Open(ckptDir)
	if err != nil {
		return nil, err
	}
	mapper, err := audio.BuildMapper(audio.LayoutStereo, audio.LayoutStereo, audio.PolicyZeroFill)
	if err != nil {
		return nil, err
	}
	p := &Pipeline{
		cfg:     cfg,
		unwrap:  clock.NewUnwrapper(0),
		drift:   clock.NewDriftEstimator(64),
		wm:      &timeline.Watermark{},
		replay:  timeline.NewReplayLog(),
		mapper:  mapper,
		loud:    audio.NewLoudness(cfg.TargetDBFS, cfg.Ceiling, cfg.WindowFrames),
		store:   store,
		outFile: outputPath,
		hash:    sha256.New(),
	}
	if cp := store.Latest(); cp != nil {
		if err := p.restore(cp.Payload); err != nil {
			return nil, err
		}
		if err := p.rehashFromOutput(); err != nil {
			return nil, err
		}
		p.resumed = true
	} else {
		p.st = state{Config: cfg}
		p.resample, err = audio.NewResampler(cfg.InRate, cfg.OutRate, 0)
		if err != nil {
			return nil, err
		}
	}
	return p, nil
}

func (p *Pipeline) restore(payload []byte) error {
	var s state
	if err := json.Unmarshal(payload, &s); err != nil {
		return clock.NewError(clock.ErrCheckpointCorrupt, err.Error())
	}
	p.st = s
	p.unwrap.Restore(s.Unwrap)
	p.drift.Restore(s.Drift)
	p.wm.Restore(s.Watermark)
	p.replay.Restore(s.Replay)
	rs, err := audio.NewResampler(s.Config.InRate, s.Config.OutRate, 0)
	if err != nil {
		return err
	}
	rs.RestorePhase(s.ResamplePhase, s.ResampleDen, s.ResampleCons, s.ResampleFrames, s.PrevFrame)
	rs.SetTotalInput(s.ResampleTotal)
	p.resample = rs
	p.loud.Restore(s.Loudness)
	p.totalIn, p.totalOut = s.TotalIn, s.TotalOut
	p.nodeOrigin, p.nodeOriginSet = s.NodeOrigin, s.NodeOriginSet
	p.hash = sha256.New()
	return nil
}

// rehashFromOutput rebuilds the sha256 from the durable output prefix so a
// resumed run's checksum continues from the uninterrupted output exactly.
func (p *Pipeline) rehashFromOutput() error {
	data, err := os.ReadFile(p.outFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	p.hash.Reset()
	p.hash.Write(data)
	return nil
}

// Metrics snapshots the current counters for CLI reporting.
func (p *Pipeline) Metrics() Metrics {
	return Metrics{
		EventCount:      p.st.EventCount,
		WatermarkNS:     p.wm.MarkNS(),
		DriftPPM:        p.drift.PPM(),
		ResampleFrames:  p.resample.FramesEmitted(),
		PeakDBFS:        p.loud.PeakDBFS(),
		OutputSHA256:    hex.EncodeToString(p.hash.Sum(nil)),
		LateEvents:      p.wm.LateCount(),
		ReplayedEvents:  p.replay.ReplayedCount(),
		SkippedDup:      p.replay.SkippedCount(),
		RecoverableErrs: p.recoverableErrs,
		Resumed:         p.resumed,
	}
}

// MarkResumed declares that this pipeline instance restored from a checkpoint.
// New sets this automatically; the setter exists for callers that construct a
// kernel and then adopt a checkpoint store explicitly.
func (p *Pipeline) MarkResumed() { p.resumed = true }

// ResumedEvents reports how many events were newly processed after the restart
// point, which is what a recovery run replays from durable state.
func (p *Pipeline) ResumedEvents() uint64 { return p.resumedEvents }

// SetSource wires the random-access source used to re-read sample payloads.
func (p *Pipeline) SetSource(r interface {
	ReadAt([]byte, int64) (int, error)
}) {
	p.payloadReader = r
}

// PendingErr returns an unrecoverable error encountered during late replay.
func (p *Pipeline) PendingErr() error { return p.pendingErr }
