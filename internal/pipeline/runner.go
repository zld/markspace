package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"time"

	"medpipe/internal/clock"
	"medpipe/internal/ingest"
	"medpipe/internal/media"
	"medpipe/internal/sample"
	"medpipe/internal/stream"
)

// Runner owns the stateful kernel subsystems for one output directory.
type Runner struct {
	opts        Options
	cfg         sample.Config
	inputDigest string

	tl        *clock.Timeline
	rtp       *ingest.RTPParser
	rtcp      *ingest.RTCPParser
	reorder   *ingest.ReorderBuffer
	segmenter *media.Segmenter
	silence   *media.SilenceDetector
	deduper   *media.Deduper
	pipe      *stream.Pipe

	state State
}

func NewRunner(opts Options, cfg sample.Config, inputDigest string) *Runner {
	cfg = cfg.WithDefaults()
	if opts.QueueBytes <= 0 {
		opts.QueueBytes = 1 << 20
	}
	if opts.CheckpointEvery <= 0 {
		opts.CheckpointEvery = 500
	}
	if opts.SegmentDuration <= 0 {
		opts.SegmentDuration = 4 * time.Second
	}
	if opts.SegmentMaxDuration <= 0 {
		opts.SegmentMaxDuration = opts.SegmentDuration + time.Second
	}
	if opts.SilenceThreshold == 0 {
		opts.SilenceThreshold = 500
	}
	if opts.SilenceMinDuration <= 0 {
		opts.SilenceMinDuration = 500 * time.Millisecond
	}

	tl := clock.NewTimeline(cfg.SampleRate)
	r := &Runner{
		opts:        opts,
		cfg:         cfg,
		inputDigest: inputDigest,
		tl:          tl,
		rtp:         ingest.NewRTPParser(cfg.SampleRate),
		rtcp:        ingest.NewRTCPParser(),
		reorder:     ingest.NewReorderBuffer(ingest.ReorderConfig{GapThreshold: 64}),
		segmenter:   media.NewSegmenter(media.SegmentConfig{Duration: opts.SegmentDuration, MaxDuration: opts.SegmentMaxDuration}),
		silence:     media.NewSilenceDetector(media.SilenceConfig{EnergyThreshold: opts.SilenceThreshold, MinDuration: opts.SilenceMinDuration}),
		deduper:     media.NewDeduper(),
		pipe:        stream.NewPipe(opts.QueueBytes),
	}
	r.state.NextSourceOffset = int64(len(sample.FileMagic))
	r.state.ensureSlices()
	return r
}

func (r *Runner) Pipe() *stream.Pipe { return r.pipe }

func (r *Runner) saveCheckpoint(completed bool, outputSHA string) error {
	r.state.Timeline = r.tl.State()
	r.state.Reorder = r.reorder.State()
	r.state.Segmenter = r.segmenter.State()
	r.state.Silence = r.silence.State()
	r.state.Deduper = r.deduper.State()
	r.state.Stats.Duplicates = r.reorder.Stats.Duplicates
	r.state.Stats.Missing = len(r.reorder.Missing)
	r.state.Stats.OutOfOrder = r.reorder.Stats.OutOfOrder
	r.state.Stats.ShortSilenceRuns = r.silence.ShortRuns()

	rawState, err := json.Marshal(r.state)
	if err != nil {
		return err
	}
	cp := clock.NewCheckpoint(r.inputDigest, ParserMajor, r.opts.CheckpointEvery)
	cp.SSRC = r.cfg.SSRC
	cp.LastSequence = r.state.LastSequence
	cp.LastExtSequence = r.state.LastExtSeq
	cp.LastByteOffset = r.state.NextSourceOffset
	cp.ProcessedPackets = r.state.ProcessedChunks
	cp.Completed = completed
	cp.OutputSHA256 = outputSHA
	cp.GeneratorConfig = marshalGeneratorConfig(r.cfg)
	cp.State = rawState
	return clock.SaveCheckpoint(checkpointPath(r.opts.OutDir), cp)
}

// HashFile streams a file through SHA-256 without loading all of it into the
// queue budgeted working set.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
