package session

import (
	"context"
	"fmt"
	"time"

	"rtspcheck/internal/frame"
	"rtspcheck/internal/report"
	"rtspcheck/internal/rtp"
	"rtspcheck/internal/transport"
)

// Config parameterizes one validation run.
type Config struct {
	Mode            transport.Mode
	URL             string
	ReorderWindow   int
	MaxFramePackets int
	MaxBufferedFrames int
	// StreamTimeout bounds how long the runner waits for RTP after PLAY.
	StreamTimeout time.Duration
	// NegotiationTimeout caps individual RTSP request/response waits.
	NegotiationTimeout time.Duration
	UDPMinPort         int
	UDPMaxPort         int
	Seed               int64
	FramesRequested    int
	// ExpectedPackets is the deterministic number of datagrams the
	// source will deliver; used as a fast completion check.
	ExpectedPackets int
	// SentCh, when non-nil, is closed once the source has finished
	// writing its full schedule. The runner then waits one quiet
	// drain window before ending the receive phase, tolerating
	// transport-level (e.g. UDP) losses.
	SentCh <-chan struct{}
}

// PhaseError annotates an error with the RTSP phase that failed, so
// timeouts and interruptions remain attributable to the right stage.
type PhaseError struct {
	Phase string
	Err   error
}

func (e *PhaseError) Error() string { return fmt.Sprintf("%s: %v", e.Phase, e.Err) }
func (e *PhaseError) Unwrap() error { return e.Err }

// Runner executes the full RTSP validation lifecycle.
type Runner struct {
	cfg        Config
	rawHandler transport.RawHandler
}

// NewRunner validates the configuration and returns a runner.
func NewRunner(cfg Config) (*Runner, error) {
	if cfg.ReorderWindow < 1 {
		return nil, fmt.Errorf("session: reorder window must be positive")
	}
	if cfg.StreamTimeout <= 0 {
		cfg.StreamTimeout = 10 * time.Second
	}
	if cfg.NegotiationTimeout <= 0 {
		cfg.NegotiationTimeout = 5 * time.Second
	}
	return &Runner{cfg: cfg}, nil
}

// Run performs OPTIONS -> DESCRIBE -> SETUP -> PLAY -> receive ->
// TEARDOWN and returns the assembled report. On any failure it drives
// the state machine to Failed, releases every connection/socket, then
// closes the machine and still returns a report alongside the error.
func (r *Runner) Run(ctx context.Context) (report.Report, error) {
	machine := NewMachine()

	adapter, err := transport.New(transport.Config{
		Mode:         r.cfg.Mode,
		Host:         hostFromURL(r.cfg.URL),
		UDPMinPort:   r.cfg.UDPMinPort,
		UDPMaxPort:   r.cfg.UDPMaxPort,
		ReadTimeout:  r.cfg.NegotiationTimeout,
		WriteTimeout: r.cfg.NegotiationTimeout,
	})
	if err != nil {
		_ = machine.Transition(EventFail)
		_ = machine.Transition(EventRetryClosed)
		return r.buildReport(machine, frame.Stats{}, rtp.Stats{}, 0, 0), err
	}

	reorderer, err := rtp.NewReorderer(r.cfg.ReorderWindow)
	if err != nil {
		_ = adapter.Close()
		_ = machine.Transition(EventFail)
		_ = machine.Transition(EventRetryClosed)
		return r.buildReport(machine, frame.Stats{}, rtp.Stats{}, 0, 0), err
	}
	assembler := frame.NewAssembler(r.cfg.MaxFramePackets, r.cfg.MaxBufferedFrames)

	var malformed int
	r.rawHandler = func(raw []byte) {
		pkt, perr := rtp.Parse(raw)
		if perr != nil {
			malformed++
			return
		}
		for _, ordered := range reorderer.Push(pkt) {
			_, _ = assembler.Push(ordered)
		}
	}

	runErr := r.negotiateAndReceive(ctx, machine, adapter, reorderer, assembler)

	// TEARDOWN + socket/connection cleanup always runs, even after failure.
	closeErr := adapter.Close()

	if runErr == nil && closeErr != nil {
		runErr = &PhaseError{Phase: "teardown", Err: closeErr}
	}

	// Drain anything still buffered; gaps and incomplete frames are
	// recorded rather than silently dropped.
	for _, p := range reorderer.Flush() {
		_, _ = assembler.Push(p)
	}
	assembler.Flush()

	r.finishMachine(machine, runErr)

	rep := r.buildReport(machine, assembler.Stats(), reorderer.Stats(), malformed, 0)
	return rep, runErr
}
