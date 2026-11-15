package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"transcode/internal/fixture"
	"transcode/internal/orchestrator"
	"transcode/internal/state"
)

// App wires parsed options into an engine run and returns an exit code.
type App struct {
	Stdout io.Writer
	Stderr io.Writer
}

// NewApp returns an App bound to the process streams.
func NewApp() *App {
	return &App{Stdout: os.Stdout, Stderr: os.Stderr}
}

// Run executes a parsed command and returns the process exit code.
func (a *App) Run(o Options) int {
	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	samples, err := a.prepareInput(o)
	if err != nil {
		return a.fail(ExitInvalidInput, err)
	}

	engine, err := orchestrator.New(orchestrator.Config{
		JobID:           jobID(o),
		WorkspaceRoot:   o.Workspace,
		InputPath:       o.InputPath,
		OutputDir:       o.OutputDir,
		WorkDir:         o.WorkDir,
		CheckpointPath:  o.Checkpoint,
		DurationSec:     o.DurationSec,
		SegmentSec:      o.SegmentSec,
		TargetKbps:      o.TargetKbps,
		MinKbps:         o.MinKbps,
		Workers:         o.Workers,
		MaxOutputBytes:  o.MaxOutputBytes,
		EncoderTemplate: o.Encoder,
		Samples:         samples,
		Params:          o.Params(),
	})
	if err != nil {
		if errors.Is(err, state.ErrCorruptCheckpoint) || errors.Is(err, state.ErrUnrecoverable) {
			return a.fail(ExitCheckpointBad, err)
		}
		return a.fail(ExitInvalidInput, err)
	}

	res, err := engine.Run(ctx)
	if err != nil {
		if errors.Is(err, state.ErrCorruptCheckpoint) || errors.Is(err, state.ErrUnrecoverable) {
			return a.fail(ExitCheckpointBad, err)
		}
		return a.fail(ExitInvalidInput, err)
	}
	if res.CheckpointError != nil {
		return a.fail(ExitCheckpointBad, res.CheckpointError)
	}

	report := BuildReport(res, o.TargetKbps)
	if o.JSON {
		if err := WriteJSON(a.Stdout, report); err != nil {
			return a.fail(ExitGeneric, err)
		}
	} else {
		WriteText(a.Stdout, report)
	}

	switch {
	case res.Interrupted > 0:
		return ExitPartialFailure
	case res.Failed > 0:
		return ExitPartialFailure
	default:
		return ExitOK
	}
}

// prepareInput regenerates the deterministic fixture. The fixture is
// content-stable, so regeneration never invalidates a prior checkpoint; the
// fingerprint check inside recovery still guards any real drift.
func (a *App) prepareInput(o Options) ([]float64, error) {
	if err := os.MkdirAll(o.Workspace, 0o755); err != nil {
		return nil, err
	}
	v, generated, err := fixture.Generate(fixture.Spec{
		Path:        o.InputPath,
		SidecarPath: "",
		DurationSec: o.DurationSec,
		FPS:         fixtureFPS,
		Width:       fixtureWidth,
		Height:      fixtureHeight,
	})
	if err != nil {
		return nil, fmt.Errorf("generate sample: %w", err)
	}
	if o.SampleSidecar != "" {
		loaded, err := o.LoadOrGenerateSamples()
		if err != nil {
			return nil, fmt.Errorf("load samples: %w", err)
		}
		if len(loaded) != len(v.Frames) {
			return nil, fmt.Errorf("%w: sample count %d does not match frame count %d",
				errInvalid, len(loaded), len(v.Frames))
		}
		return loaded, nil
	}
	return generated, nil
}

func (a *App) fail(code int, err error) int {
	fmt.Fprintf(a.Stderr, "transcode: %v\n", err)
	return code
}
