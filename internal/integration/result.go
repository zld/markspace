package integration

import (
	"context"
	"errors"
	"time"

	relaypkg "looprelay/internal/relay"
	"looprelay/internal/report"
	rtcpkg "looprelay/internal/rtc"
	"looprelay/internal/signal"
)

// Exit codes shared with main.
const (
	ExitOK          = 0
	ExitInvalidArgs = 2
	ExitSignaling   = 3
	ExitIntegrity   = 4
	ExitInterrupted = 130
)

// ErrPacketCountMismatch flags a counter disagreement.
var ErrPacketCountMismatch = errors.New("packet counters disagree")

// Result is the outcome of one loopback demo run.
type Result struct {
	Report report.Report
	Exit   int
	Err    error
}

func classifyExit(err error) int {
	var loopErr *rtcpkg.ErrNonLoopbackCandidate
	switch {
	case err == nil:
		return ExitOK
	case errors.As(err, &loopErr):
		return ExitSignaling
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return ExitInterrupted
	case errors.Is(err, rtcpkg.ErrConnectFailed),
		errors.Is(err, rtcpkg.ErrConnectTimeout),
		errors.Is(err, rtcpkg.ErrGatheringTimeout),
		errors.Is(err, rtcpkg.ErrNoSelectedPair),
		errors.Is(err, signal.ErrNegotiationTimeout),
		errors.Is(err, signal.ErrOutOfOrder),
		errors.Is(err, signal.ErrDuplicateRequest),
		errors.Is(err, signal.ErrExpiredRequest),
		errors.Is(err, signal.ErrAborted):
		return ExitSignaling
	case errors.Is(err, relaypkg.ErrQueueFull),
		errors.Is(err, relaypkg.ErrWriteFailed),
		errors.Is(err, ErrPacketCountMismatch):
		return ExitIntegrity
	default:
		return ExitSignaling
	}
}

func shutdownServer(srv *signal.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}

func failure(exit int, err error) Result {
	return Result{
		Exit: exit,
		Err:  err,
		Report: report.Report{
			Status: report.StatusFailed,
			Error:  err.Error(),
		},
	}
}
