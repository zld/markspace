package session

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/bluenviron/gortsplib/v4/pkg/base"

	"rtspcheck/internal/frame"
	"rtspcheck/internal/report"
	"rtspcheck/internal/rtp"
	"rtspcheck/internal/transport"
)

// negotiateAndReceive runs the RTSP methods and the receive loop. The
// state machine is advanced exactly once per phase so TCP and UDP
// produce identical transition counts on the equivalent path.
func (r *Runner) negotiateAndReceive(
	ctx context.Context,
	machine *Machine,
	adapter transport.Adapter,
	reorderer *rtp.Reorderer,
	assembler *frame.Assembler,
) error {
	u, err := base.ParseURL(r.cfg.URL)
	if err != nil {
		return r.fail(machine, "parse-url", err)
	}

	// Connecting: TCP connect plus OPTIONS.
	if err := machine.Transition(EventConnect); err != nil {
		return r.fail(machine, "state", err)
	}
	client := adapter.Client()
	if err := client.Start(u.Scheme, u.Host); err != nil {
		return r.fail(machine, "connect", err)
	}
	adapter.MarkStarted()
	if _, err := client.Options(u); err != nil {
		return r.fail(machine, "options", err)
	}

	// Described.
	desc, res, err := client.Describe(u)
	if err != nil {
		return r.fail(machine, "describe", err)
	}
	if res == nil || res.StatusCode != base.StatusOK {
		return r.fail(machine, "describe", unexpectedStatus(res))
	}
	if err := machine.Transition(EventDescribeOK); err != nil {
		return r.fail(machine, "state", err)
	}

	// Setup.
	if err := client.SetupAll(desc.BaseURL, desc.Medias); err != nil {
		return r.fail(machine, "setup", err)
	}
	// gortsplib stores RTP callbacks on the setupped media, so the
	// handler must be attached after SETUP and before PLAY.
	adapter.OnRTP(r.rawHandler)
	if err := machine.Transition(EventSetupOK); err != nil {
		return r.fail(machine, "state", err)
	}

	// Play.
	res, err = client.Play(nil)
	if err != nil {
		return r.fail(machine, "play", err)
	}
	if res == nil || res.StatusCode != base.StatusOK {
		return r.fail(machine, "play", unexpectedStatus(res))
	}
	if err := machine.Transition(EventPlayOK); err != nil {
		return r.fail(machine, "state", err)
	}

	// Receive until the expected number of frames is reassembled,
	// the connection fails, or the stream timeout elapses. The mock
	// keeps the control connection open after sending, so completion
	// is driven by frame count rather than by connection close.
	waitCh := make(chan error, 1)
	go func() { waitCh <- client.Wait() }()

	streamCtx, cancel := context.WithTimeout(ctx, r.cfg.StreamTimeout)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	// Once the source signals end-of-stream, allow a short drain
	// window for in-flight UDP datagrams to arrive, then finish even
	// if some were lost on the transport.
	var drain *time.Timer
	drainCh := func() <-chan time.Time {
		if drain == nil {
			return nil
		}
		return drain.C
	}
	streamDone := r.cfg.SentCh

	for {
		// Fast path: every scheduled packet was received (TCP case).
		if reorderer.Stats().Received >= r.cfg.ExpectedPackets {
			return r.stopAndClose(machine)
		}
		select {
		case err := <-waitCh:
			if err != nil {
				return r.fail(machine, "receive", err)
			}
			return r.fail(machine, "receive", errors.New("session ended before stream completed"))
		case <-streamDone:
			// Arm the drain window once; nil the channel so this
			// case does not re-fire.
			if drain == nil {
				drain = time.NewTimer(300 * time.Millisecond)
			}
			streamDone = nil
		case <-drainCh():
			return r.stopAndClose(machine)
		case <-ticker.C:
			// Re-check the fast path on the next loop iteration.
		case <-streamCtx.Done():
			if ctx.Err() != nil {
				return r.fail(machine, "receive", ctx.Err())
			}
			return r.fail(machine, "receive", streamCtx.Err())
		}
	}
}

// stopAndClose drives the last two happy-path transitions. TEARDOWN
// itself is issued later by adapter.Close in Run.
func (r *Runner) stopAndClose(machine *Machine) error {
	if err := machine.Transition(EventStop); err != nil {
		return r.fail(machine, "state", err)
	}
	if err := machine.Transition(EventClosed); err != nil {
		return r.fail(machine, "state", err)
	}
	return nil
}

// fail moves the machine to Failed while preserving the phase in the
// returned error. The eventual RetryClosed transition is performed by
// finishMachine after resource cleanup.
func (r *Runner) fail(machine *Machine, phase string, err error) error {
	_ = machine.Transition(EventFail)
	return &PhaseError{Phase: phase, Err: err}
}

// finishMachine guarantees a terminal Closed state. On the happy path
// the machine is already Closed; on failure it moves Failed -> Closed
// after cleanup has released the resources.
func (r *Runner) finishMachine(machine *Machine, runErr error) {
	if runErr != nil && machine.State() == StateFailed {
		_ = machine.Transition(EventRetryClosed)
	}
}

func (r *Runner) buildReport(
	machine *Machine,
	fs frame.Stats,
	rs rtp.Stats,
	malformed int,
	_ int,
) report.Report {
	return report.Report{
		Mode:              string(r.cfg.Mode),
		StateTransitions:  machine.TransitionCount(),
		IllegalRejections: machine.IllegalRejections(),
		ReceivedPackets:   rs.Received,
		SequenceGaps:      rs.Gaps,
		DuplicatePackets:  rs.Duplicates,
		LatePackets:       rs.Late,
		MalformedPackets:  malformed,
		CompleteFrames:    fs.Complete,
		IncompleteFrames:  fs.Incomplete,
		EvictedFrames:     fs.Evicted,
		FinalState:        string(machine.State()),
		Seed:              r.cfg.Seed,
		FramesRequested:   r.cfg.FramesRequested,
	}
}

func unexpectedStatus(res *base.Response) error {
	if res == nil {
		return &PhaseError{Phase: "status", Err: errors.New("no response received")}
	}
	return &PhaseError{
		Phase: "status",
		Err:   fmt.Errorf("unexpected status code %d", res.StatusCode),
	}
}

func hostFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
}
