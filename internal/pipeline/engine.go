package pipeline

import (
	"fmt"
	"os"
	"path/filepath"

	"mediavault/internal/archive"
	"mediavault/internal/fixture"
	"mediavault/internal/ingest"
	"mediavault/internal/repair"
	"mediavault/internal/report"
	"mediavault/internal/timeline"
)

// Fault scenarios supported by the demo adapter.
const (
	FaultNone         = "none"
	FaultTruncatedBox = "truncated-box"
	FaultBoundaryJump = "boundary-jump"
)

// Config controls one engine run.
type Config struct {
	WorkDir        string
	Fault          string
	InterruptAfter int // flush this many chunks then stop with ErrInterrupted
	ChunkSize      int
}

// ErrInterrupted signals a deliberately injected chunk-write interruption.
var ErrInterrupted = fmt.Errorf("chunk write interrupted after configured chunk count")

// Engine wires the four stages over project-generated fixtures.
type Engine struct {
	cfg Config
}

// NewEngine builds an engine for the supplied config.
func NewEngine(cfg Config) *Engine {
	if cfg.ChunkSize <= 0 {
		cfg.ChunkSize = 4
	}
	return &Engine{cfg: cfg}
}

// demoSpec builds the deterministic two-track source used by every run.
func demoSpec() fixture.MP4Spec {
	return fixture.MP4Spec{Tracks: []fixture.MediaSpec{
		{Kind: 'v', Timescale: 30000, FrameCount: 8, SampleSize: 32, FrameDelta: 1001, Key: 1},
		{Kind: 'a', Timescale: 48000, FrameCount: 6, SampleSize: 24, FrameDelta: 1024, Key: 9},
	}}
}

// prepareInput writes the MP4 fixture, applying the truncation fault when
// requested. It returns the input path and any structural ingest error.
func (e *Engine) prepareInput() (string, error) {
	dir := filepath.Join(e.cfg.WorkDir, "input")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "source.mp4")
	full, err := fixture.BuildMP4(demoSpec())
	if err != nil {
		return "", err
	}
	if e.cfg.Fault == FaultTruncatedBox {
		// Cross-box truncation: cut inside mdat, keeping box headers valid.
		cut := len(full) - 12
		return path, fixture.TruncateFile(path, full, cut)
	}
	return path, os.WriteFile(path, full, 0o644)
}

// Run executes ingest -> normalize -> fingerprint -> archive, honoring the
// interruption policy. A recoverable per-track error is reported but does
// not abort sibling tracks; an unrecoverable/structural error is returned.
func (e *Engine) Run() (*report.Metrics, error) {
	b := report.NewBuilder(e.cfg.Fault)
	inputPath, err := e.prepareInput()
	if err != nil {
		return nil, err
	}

	// Stage 1+2: ingest and normalize per track.
	parsed, err := ingest.ParseMP4(inputPath, "demo")
	if err != nil {
		return nil, err
	}

	idx, err := archive.OpenIndex(e.cfg.WorkDir, e.cfg.ChunkSize)
	if err != nil {
		return nil, err
	}
	if len(idx.Chunks()) > 0 {
		b.MarkRecovered()
	}

	rep := repair.NewBoundaryRepairer(1 << 33)
	var fatal *timeline.StageError
	var rows []archive.ManifestRow

	// RTP side-channel: exercises 32-bit unwrap and, under the fault,
	// a source-switch boundary repair. Its samples join the archive too.
	rtpSamples, _ := e.ingestRTP(rep, b)
	if len(rtpSamples) > 0 {
		rtpPT := &ingest.ParsedTrack{
			ID:        timeline.TrackID{Source: "rtp", Index: 0, Kind: timeline.KindVideo},
			Timescale: 90000,
			Samples:   rtpSamples,
		}
		rtpRows, ferr := e.processTrack(rtpPT, rep, b)
		if ferr != nil {
			fatal = ferr
		}
		rows = append(rows, rtpRows...)
	}

	for _, pt := range parsed.Tracks {
		for _, se := range pt.Errors {
			b.AddError(report.ErrorView{
				Stage: se.Stage, Kind: se.Kind, Offset: se.Offset,
				Position: se.Position, Track: se.Track.String(),
				Recoverable: se.Recoverable, Detail: se.Detail,
			})
			if !se.Recoverable {
				fatal = se
			}
		}
		if len(pt.Samples) == 0 {
			continue
		}
		trackRows, ferr := e.processTrack(pt, rep, b)
		if ferr != nil {
			fatal = ferr
		}
		rows = append(rows, trackRows...)
	}

	// Stage 3+4: fingerprint then archive, chunked with checkpoints.
	chunks, ierr := e.archiveRows(idx, rows, b)
	b.SetChunks(chunks)
	b.SetCorrections(rep.Corrections())
	b.SetFailed(rep.Failed())
	if ierr != nil {
		return metricPtr(b), ierr
	}
	if fatal != nil {
		b.MarkFailed()
		return metricPtr(b), fatal
	}
	return metricPtr(b), nil
}

func metricPtr(b *report.Builder) *report.Metrics {
	m := b.Build()
	return &m
}
