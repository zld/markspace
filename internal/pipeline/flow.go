// Package pipeline connects processing stages with bounded channels and
// enforces a single failure/cancellation contract: when any stage fails or the
// caller cancels, every stage stops and observes the same root cause.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"mediapipe/internal/model"
)

// Stage is one unit of work driven by Run.
type Stage[T any] struct {
	Name string
	// Run processes items from in, sends results to out, and must send
	// exactly one summary to each of the two result channels carrying the
	// same error value (nil on success). It must close out when it returns.
	Run func(ctx context.Context, in <-chan T, out chan<- T) error
	// Work is the per-item transform used by New-designed stages. Supplying
	// Run instead allows stateful or fan-in stages.
	Work func(ctx context.Context, item T) ([]T, error)
}

// Config controls buffering and the watchdog.
type Config struct {
	// Buffer is the depth of every inter-stage channel. A full channel
	// blocks the producer: that is the backpressure mechanism.
	Buffer int
	// FailFast stops the chain at the first error. It is always on; the
	// field exists to make the guarantee explicit in reports.
	FailFast bool
}

// DefaultConfig backs every channel with one slot so a slow consumer stalls
// its producer almost immediately.
func DefaultConfig() Config { return Config{Buffer: 1, FailFast: true} }

// Result is the terminal outcome of a run.
type Result[T any] struct {
	Items []T
	Stats Stats
	Err   error
}

// Stats reports throughput and timing for the report stage.
type Stats struct {
	StageLatency map[string]Latency
	Started      time.Time
	Finished     time.Time
	Stages       int
}

// Latency summarises per-stage service time.
type Latency struct {
	Count  int
	MeanUS int64
	MaxUS  int64
	P95US  int64
}

// Run drives stages in order over the same element type. Every stage runs in
// its own goroutine; channels are bounded by cfg.Buffer. The first error (or
// context cancellation) is recorded once and propagated to all stages, which
// then stop. Run returns after every goroutine has exited.
func Run[T any](ctx context.Context, cfg Config, stages []Stage[T], input []T) Result[T] {
	res := Result[T]{Stats: Stats{StageLatency: map[string]Latency{}, Started: time.Now(), Stages: len(stages)}}
	if cfg.Buffer <= 0 {
		cfg.Buffer = 1
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type link struct {
		in  chan T
		out chan T
	}
	links := make([]link, len(stages))
	first := make(chan T, cfg.Buffer)
	for i := range stages {
		out := make(chan T, cfg.Buffer)
		links[i] = link{in: first, out: out}
		first = out
	}
	final := links[len(links)-1].out

	var (
		mu       sync.Mutex
		rootErr  error
		latency  = map[string][]int64{}
		wgStages sync.WaitGroup
	)
	recordErr := func(err error) {
		if err == nil {
			return
		}
		mu.Lock()
		if rootErr == nil {
			rootErr = err
			cancel()
		}
		mu.Unlock()
	}
	recordLatency := func(name string, us int64) {
		mu.Lock()
		latency[name] = append(latency[name], us)
		mu.Unlock()
	}

	wgStages.Add(len(stages))
	for i := range stages {
		st := stages[i]
		lk := links[i]
		go func() {
			defer wgStages.Done()
			defer close(lk.out)
			err := runStage(ctx, st, lk.in, lk.out, recordLatency)
			recordErr(normaliseError(st.Name, err))
		}()
	}

	// Producer feeds the first stage; it is a stage in its own right for
	// cancellation purposes but carries no latency cost of its own.
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		defer close(links[0].in)
		for _, item := range input {
			select {
			case <-ctx.Done():
				recordErr(normaliseError("producer", ctxErr(ctx)))
				return
			case links[0].in <- item:
			}
		}
	}()

	// Consumer drains the final stage so no stage blocks forever on a full
	// output channel when the consumer itself has stopped.
	var items []T
	consumeDone := make(chan struct{})
	go func() {
		defer close(consumeDone)
		for item := range final {
			items = append(items, item)
		}
	}()

	<-producerDone
	wgStages.Wait()
	<-consumeDone

	mu.Lock()
	res.Err = rootErr
	for name, samples := range latency {
		res.Stats.StageLatency[name] = summarise(samples)
	}
	mu.Unlock()
	res.Items = items
	res.Stats.Finished = time.Now()
	return res
}

// normaliseError guarantees that every stage failure reaching the caller is a
// *model.Error, so the report and exit code can rely on stage/kind even when a
// stage returned a bare error or a plain context cancellation.
func normaliseError(stage string, err error) error {
	if err == nil {
		return nil
	}
	var me *model.Error
	if errors.As(err, &me) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return model.New(model.KindCancelled, model.StagePipeline, "", stage, err)
	}
	return model.New(model.KindInternal, model.StagePipeline, "", stage, err)
}

func ctxErr(ctx context.Context) error {
	cause := context.Cause(ctx)
	if cause == nil {
		cause = ctx.Err()
	}
	if cause == nil {
		cause = errors.New("cancelled")
	}
	return cause
}

func runStage[T any](ctx context.Context, st Stage[T], in <-chan T, out chan<- T, record func(string, int64)) error {
	if st.Run != nil {
		return st.Run(ctx, in, out)
	}
	if st.Work == nil {
		return model.New(model.KindInternal, model.StagePipeline, "", st.Name,
			errors.New("stage has neither Run nor Work"))
	}
	for {
		select {
		case <-ctx.Done():
			return ctxErr(ctx)
		case item, ok := <-in:
			if !ok {
				return nil
			}
			start := time.Now()
			results, err := st.Work(ctx, item)
			record(st.Name, time.Since(start).Microseconds())
			if err != nil {
				return err
			}
			for _, r := range results {
				select {
				case <-ctx.Done():
					return ctxErr(ctx)
				case out <- r:
				}
			}
		}
	}
}

func summarise(samples []int64) Latency {
	if len(samples) == 0 {
		return Latency{}
	}
	sorted := append([]int64(nil), samples...)
	sortInt64(sorted)
	var sum int64
	for _, s := range sorted {
		sum += s
	}
	p95 := sorted[(len(sorted)*95)/100]
	if p95 == 0 && len(sorted) > 0 {
		p95 = sorted[len(sorted)-1]
	}
	return Latency{
		Count:  len(sorted),
		MeanUS: sum / int64(len(sorted)),
		MaxUS:  sorted[len(sorted)-1],
		P95US:  p95,
	}
}

func sortInt64(s []int64) {
	// Insertion sort is adequate for the small per-stage sample counts used
	// by the demo; avoid pulling in sort for a single call site.
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// ErrCancelled reports whether the result was stopped by cancellation rather
// than a stage failure.
func (r Result[T]) ErrCancelled() bool {
	var me *model.Error
	return errors.As(r.Err, &me) && me.Kind == model.KindCancelled
}

// CauseOf returns the single root error, unwrapping the pipeline's own
// cancellation wrapper so every stage reports the same reason.
func CauseOf(err error) error {
	if err == nil {
		return nil
	}
	var me *model.Error
	if errors.As(err, &me) && me.Kind == model.KindCancelled && me.Cause != nil {
		return me.Cause
	}
	return err
}

// ValidateStages checks a chain before any goroutine starts.
func ValidateStages[T any](stages []Stage[T]) error {
	if len(stages) == 0 {
		return model.New(model.KindInvalid, model.StagePipeline, "", "validate",
			errors.New("pipeline has no stages"))
	}
	seen := map[string]bool{}
	for i, s := range stages {
		if s.Name == "" {
			return model.New(model.KindInvalid, model.StagePipeline, "", "validate",
				fmt.Errorf("stage %d has no name", i))
		}
		if seen[s.Name] {
			return model.New(model.KindInvalid, model.StagePipeline, "", "validate",
				fmt.Errorf("duplicate stage name %q", s.Name))
		}
		seen[s.Name] = true
		if s.Run == nil && s.Work == nil {
			return model.New(model.KindInvalid, model.StagePipeline, s.Name, "validate",
				errors.New("stage has neither Run nor Work"))
		}
	}
	return nil
}
