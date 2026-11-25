package pipeline

import (
	"context"
	"errors"

	"archivecheck/internal/diagnostic"
)

// Status is the terminal state of one input.
type Status string

const (
	StatusSuccess    Status = "success"
	StatusFailed     Status = "failed"
	StatusCanceled   Status = "canceled"
	StatusNotStarted Status = "not_started"
)

// ErrCancelAfterBudget is the deterministic cause used by CancelAfterCompleted.
var ErrCancelAfterBudget = errors.New("cancellation requested after completed-item budget")

// Controller propagates one cancellation cause through producer, queue, and
// workers.
type Controller struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
}

// NewController creates a cancellable controller derived from parent.
func NewController(parent context.Context) *Controller {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancelCause(parent)
	return &Controller{ctx: ctx, cancel: cancel}
}

// Context returns the context observed by all pipeline stages.
func (c *Controller) Context() context.Context { return c.ctx }

// Cancel stops acceptance of new work.
func (c *Controller) Cancel(reason error) {
	if reason == nil {
		reason = context.Canceled
	}
	c.cancel(reason)
}

func cancellationDiagnostic(job Job, cause error) *diagnostic.Error {
	if de, ok := diagnostic.From(cause); ok {
		return de
	}
	code := diagnostic.CodeCanceled
	if errors.Is(cause, context.DeadlineExceeded) {
		code = diagnostic.CodeCanceled
	}
	return diagnostic.Wrap(code, diagnostic.PhaseCancel, job.Sample, 0, cause,
		"job %d cancelled before completion", job.Index)
}
