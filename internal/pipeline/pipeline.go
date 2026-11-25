// Package pipeline wires the container, stream, transport, transaction
// and report subsystems into one deterministic, restartable processing
// run. It contains orchestration only; every substantive behavior lives
// in the subsystem packages.
package pipeline

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/local/medialab/internal/container"
	"github.com/local/medialab/internal/fixture"
	"github.com/local/medialab/internal/model"
	"github.com/local/medialab/internal/report"
	rtpx "github.com/local/medialab/internal/rtp"
	"github.com/local/medialab/internal/stream"
	"github.com/local/medialab/internal/timeline"
	"github.com/local/medialab/internal/txn"
)

// Config captures the demo and test parameters in one value.
type Config struct {
	Seed         int64
	LossBurst    int
	LossAt       int
	Reorder      int
	Duplicate    int
	Bandwidth    []int
	InterruptAt  string // e.g. "commit-1", "" disables injection
	WorkDir      string
	// DamageKind selects the MP4 mutation for the damaged sample; zero is
	// truncation.
	DamageKind fixture.Corruption
}

// Stage names double as commit boundary ids.
const (
	StageContainer = "commit-0"
	StageStream    = "commit-1"
	StageTransport = "commit-2"
	StageReport    = "commit-3"
)

// Result is what the CLI receives after a run (or a resumed run).
type Result struct {
	Summary     report.Summary
	ContentHash string
	JournalDir  string
	ErrorBook   *model.ErrorBook
}

// Run executes the full four-stage pipeline. On a second invocation with
// the same WorkDir, committed stages are preserved and only missing work
// runs; the summary and content hash stay identical.
func Run(cfg Config) (Result, error) {
	if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("pipeline: workdir: %w", err)
	}
	book := &model.ErrorBook{}
	state := map[string][]byte{}

	journal, err := txn.OpenJournal(filepath.Join(cfg.WorkDir, "txn"))
	if err != nil {
		return Result{}, err
	}
	defer journal.Close()

	hook := crashHook(cfg.InterruptAt)
	coord := txn.NewCoordinator(journal, hook)

	ctx := &runContext{cfg: cfg, book: book}
	stages := []txn.Stage{
		{
			Name: StageContainer,
			Run:  ctx.stageContainer,
			Validate: func(b []byte) error {
				if len(b) == 0 {
					return errors.New("container stage produced no output")
				}
				return nil
			},
		},
		{Name: StageStream, Run: ctx.stageStream, Validate: nonEmpty},
		{Name: StageTransport, Run: ctx.stageTransport, Validate: nonEmpty},
		{Name: StageReport, Run: ctx.stageReport, Validate: nonEmpty},
	}
	if err := coord.Run(stages, state); err != nil {
		return Result{JournalDir: cfg.WorkDir, ErrorBook: book}, err
	}

	summary := ctx.summary
	summary.CommitID = journal.Boundary()
	hash := report.Hash(summary)
	return Result{
		Summary: summary, ContentHash: hash,
		JournalDir: cfg.WorkDir, ErrorBook: book,
	}, nil
}

func nonEmpty(b []byte) error {
	if len(b) == 0 {
		return errors.New("stage produced empty output")
	}
	return nil
}

// crashHook converts the CLI's "commit-N" interruption point into a
// pre-commit fault on that exact stage.
func crashHook(interruptAt string) txn.CrashHook {
	if interruptAt == "" {
		return nil
	}
	return func(stage, step string) error {
		if stage == interruptAt && step == "pre-commit" {
			return errors.New("injected interruption")
		}
		return nil
	}
}

// runContext carries per-run values shared between stages.
type runContext struct {
	cfg      Config
	book     *model.ErrorBook
	arbitr   container.ArbitratedMeta
	mp4Meta  *container.MP4Meta
	fileSize int64
	stats    stream.FrameStats
	damage   stream.DamageReport
	rtp      rtpx.SessionResult
	summary  report.Summary
}

// stageContainer generates and parses both containers and arbitrates
// conflicting metadata.
func (c *runContext) stageContainer(_ map[string][]byte) ([]byte, error) {
	mxfSpec := fixture.DefaultMXFSpec(c.cfg.Seed)
	mxfData, _ := fixture.GenerateMXF(mxfSpec)
	mxfMeta, err := container.ParseMXF(mxfData)
	if err != nil {
		return nil, err
	}
	mp4Data, _ := fixture.GenerateMP4(fixture.DefaultMP4Spec(c.cfg.Seed))
	mp4Meta, err := container.ParseMP4(mp4Data)
	if err != nil {
		return nil, err
	}
	c.mp4Meta = mp4Meta
	c.fileSize = int64(len(mp4Data))
	c.arbitr = container.Arbitrate(mxfMeta, mp4Meta, c.book)
	return []byte(fmt.Sprintf("container:tracks=%d:conflicts=%d",
		len(c.arbitr.Tracks), c.arbitr.ConflictCount)), nil
}

// stageStream scans frames, keyframe gaps and damage windows.
func (c *runContext) stageStream(_ map[string][]byte) ([]byte, error) {
	c.stats = stream.AnalyzeFrames(c.mp4Meta.Samples, c.fileSize, c.book)
	c.damage = stream.DetectDamage(c.mp4Meta, c.fileSize, c.stats, c.book)
	return []byte(fmt.Sprintf("stream:frames=%d:gapMs=%d:ranges=%d",
		len(c.stats.Frames), c.stats.KeyframeGapMs, len(c.damage.Ranges))), nil
}

// stageTransport generates, perturbs, reorders and repairs the RTP session.
func (c *runContext) stageTransport(_ map[string][]byte) ([]byte, error) {
	ordered, _ := fixture.GenerateRTPSession(c.cfg.Seed, 32, 4, 0, 90000)
	incoming := fixture.PerturbRTP(ordered, fixture.RTPPerturbation{
		Seed:      c.cfg.Seed,
		LossBurst: c.cfg.LossBurst,
		LossAt:    c.cfg.LossAt,
		Reorder:   c.cfg.Reorder,
		Duplicate: c.cfg.Duplicate,
	})
	sess := rtpx.NewSession(rtpx.DefaultReorderConfig(), c.cfg.Bandwidth)
	arrive := int64(0)
	for _, pkt := range incoming {
		sess.Push(pkt, arrive)
		arrive += timeline.MasterHz / 1000
	}
	c.rtp = sess.Finalize(c.book)
	return []byte(fmt.Sprintf("transport:delivered=%d:recovered=%d:missing=%d",
		len(c.rtp.Delivered), c.rtp.Recovery.Recovered, len(c.rtp.Missing))), nil
}

// stageReport assembles the deterministic summary from every subsystem.
func (c *runContext) stageReport(_ map[string][]byte) ([]byte, error) {
	av := stream.AVSync(c.mp4Meta)
	b := report.NewBuilder().
		SetAVSync(av).
		SetRecovery(rtpx.RecoveryRate(c.rtp.Recovery),
			c.rtp.Recovery.Recovered, c.rtp.Recovery.Unrecovered).
		SetLatency(c.rtp.Latency.P95Ms).
		SetKeyframeGap(c.stats.KeyframeGapMs).
		SetDamage(c.damage.Ranges).
		SetConflicts(c.arbitr.ConflictCount).
		SetErrorCount(len(c.book.All()))
	healthy := av <= 20 &&
		rtpx.RecoveryRate(c.rtp.Recovery) >= 0.75 &&
		c.rtp.Latency.P95Ms <= 500
	c.summary = b.MarkHealthy(healthy).Build()
	raw, err := report.StableJSON(c.summary)
	if err != nil {
		return nil, err
	}
	return raw, nil
}
