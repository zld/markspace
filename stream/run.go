package stream

import (
	"context"

	"mediarc/model"
)

// Wait closes the input, blocks until every stage has returned, and reports the
// first error. A canceled context is reported as a canceled error.
func (p *Pipeline) Wait() error {
	p.CloseInput()
	p.wg.Wait()
	if p.err != nil {
		return p.err
	}
	if err := p.ctx.Err(); err != nil {
		return newError(model.CodeCanceled, "pipeline canceled").WithCause(err)
	}
	return nil
}

// runStage drives one stage and owns closing its output channel.
func (p *Pipeline) runStage(stage StageFunc, in <-chan Item, out chan<- Item) {
	defer p.wg.Done()
	defer close(out)
	if err := stage(p.ctx, in, out); err != nil {
		p.fail(err)
		return
	}
	if err := p.ctx.Err(); err != nil {
		p.fail(newError(model.CodeCanceled, "stage canceled").WithCause(err))
	}
}

// fail records the first error and cancels the rest of the pipeline.
func (p *Pipeline) fail(err error) {
	if err == nil {
		return
	}
	p.errOnce.Do(func() { p.err = err })
	p.cancel()
}

func newError(code model.Code, detail string) *model.Error {
	return model.New(code, streamName, detail)
}

// Drain consumes everything from a channel and returns the items, or the first
// error the pipeline reported. It is a convenience for the acceptance harness.
func Drain(ctx context.Context, p *Pipeline) ([]Item, error) {
	var items []Item
	for {
		select {
		case item, ok := <-p.Output():
			if !ok {
				return items, p.Wait()
			}
			items = append(items, item)
		case <-ctx.Done():
			p.Cancel()
			_ = p.Wait()
			return items, newError(model.CodeCanceled, "drain canceled").WithCause(ctx.Err())
		}
	}
}
