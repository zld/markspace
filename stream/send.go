package stream

import (
	"context"

	"mediarc/model"
)

// Send blocks until the first stage accepts item, the pipeline fails, or ctx is
// done.
func (p *Pipeline) Send(ctx context.Context, item Item) error {
	if ctx == nil {
		ctx = context.Background()
	}
	p.sendMu.RLock()
	defer p.sendMu.RUnlock()
	if p.closed {
		return newError(model.CodeClosed, "pipeline input is closed")
	}
	if p.ctx.Err() != nil {
		if err := p.Err(); err != nil {
			return err
		}
		return newError(model.CodeCanceled, "pipeline was canceled").WithCause(p.ctx.Err())
	}
	select {
	case p.input <- item:
		return nil
	case <-ctx.Done():
		return newError(model.CodeCanceled, "send canceled").WithCause(ctx.Err())
	case <-p.ctx.Done():
		if err := p.Err(); err != nil {
			return err
		}
		return newError(model.CodeCanceled, "pipeline was canceled").WithCause(p.ctx.Err())
	}
}

// TrySend offers item without blocking. It reports false when the input queue is
// full, which lets callers observe backpressure directly.
func (p *Pipeline) TrySend(item Item) bool {
	p.sendMu.RLock()
	defer p.sendMu.RUnlock()
	if p.closed || p.ctx.Err() != nil {
		return false
	}
	select {
	case p.input <- item:
		return true
	default:
		return false
	}
}

// CloseInput marks the end of the input stream. It is safe to call twice and
// safe to race with Send: once it returns no further send can be accepted.
func (p *Pipeline) CloseInput() {
	p.closeOnce.Do(func() {
		p.sendMu.Lock()
		defer p.sendMu.Unlock()
		p.closed = true
		close(p.input)
	})
}

// Cancel stops the pipeline early. Unlike CloseInput it keeps the input open, so
// producers see a canceled code rather than a closed-channel panic.
func (p *Pipeline) Cancel() {
	p.fail(newError(model.CodeCanceled, "pipeline canceled"))
}
