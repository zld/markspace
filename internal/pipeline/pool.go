// Package pipeline runs the frame worker pool. Workers share one rational
// time base and call the transform module; duplicate sequence numbers,
// zero-length frames, time overflows and corrupt data become explicit failed
// states rather than panics. Results are sorted by time then track so the
// output manifest is independent of goroutine scheduling.
package pipeline

import (
	"sort"
	"sync"

	"continuity/internal/clock"
	"continuity/internal/journal"
	"continuity/internal/model"
	"continuity/internal/transform"
)

// Job is one unit of work handed to a worker.
type Job struct {
	Frame  model.Frame
	Params transform.Params
}

// Outcome is the deterministic result or failure for one frame.
type Outcome struct {
	Frame  model.Frame
	Output *model.OutputFrame
	Err    *model.StructuredError
}

// Pool executes jobs with a fixed number of worker goroutines and drives the
// journal state machine around each frame.
type Pool struct {
	threads int
	journal *journal.Journal
}

// New constructs a pool.
func New(threads int, j *journal.Journal) (*Pool, error) {
	if threads < 1 {
		return nil, model.NewError(model.StagePipeline, 0, 0, "worker pool needs at least one thread")
	}
	return &Pool{threads: threads, journal: j}, nil
}

// Run validates every job up front (so a corrupt input fails before any
// transform starts), then processes them concurrently and returns sorted
// outcomes. Any frame that fails is transitioned to the terminal failed state.
func (p *Pool) Run(jobs []Job) ([]Outcome, error) {
	seen := map[uint32]struct{}{}
	for i := range jobs {
		f := &jobs[i].Frame
		if err := preValidate(f, seen); err != nil {
			_ = p.journal.Transition(f.Number, f.Track, journal.StateFailed, err.Error())
			return nil, err
		}
	}

	work := make(chan Job)
	var (
		mu       sync.Mutex
		outcomes []Outcome
		wg       sync.WaitGroup
		failOnce sync.Once
		firstErr *model.StructuredError
	)

	for w := 0; w < p.threads; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range work {
				out := p.process(job)
				mu.Lock()
				outcomes = append(outcomes, out)
				mu.Unlock()
				if out.Err != nil {
					failOnce.Do(func() { firstErr = out.Err })
				}
			}
		}()
	}
	for _, job := range jobs {
		work <- job
	}
	close(work)
	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}
	sort.SliceStable(outcomes, func(i, k int) bool {
		if outcomes[i].Frame.CaptureNanos != outcomes[k].Frame.CaptureNanos {
			return outcomes[i].Frame.CaptureNanos < outcomes[k].Frame.CaptureNanos
		}
		if outcomes[i].Frame.Track != outcomes[k].Frame.Track {
			return outcomes[i].Frame.Track < outcomes[k].Frame.Track
		}
		return outcomes[i].Frame.Number < outcomes[k].Frame.Number
	})
	return outcomes, nil
}

// preValidate enforces the four explicit input failure conditions.
func preValidate(f *model.Frame, seen map[uint32]struct{}) error {
	if _, dup := seen[f.Sequence]; dup {
		return model.NewError(model.StagePipeline, f.Number, int64(f.ContainerBytes),
			"duplicate sequence number")
	}
	seen[f.Sequence] = struct{}{}
	if len(f.Payload) == 0 || f.PayloadLength == 0 {
		return model.NewError(model.StagePipeline, f.Number, int64(f.ContainerBytes),
			"zero-length frame")
	}
	if f.CaptureNanos < 0 {
		return model.NewError(model.StagePipeline, f.Number, int64(f.ContainerBytes),
			"negative capture time")
	}
	// Time overflow: the capture time must fit the shared RTP clock.
	if _, err := clock.RTPTimestamp(f.CaptureNanos, clock.MediaClock); err != nil {
		return model.WrapError(model.StagePipeline, f.Number, int64(f.ContainerBytes),
			"media time overflow", err)
	}
	if !model.VerifyChecksum(f.Payload, f.SHA256) {
		return model.NewError(model.StagePipeline, f.Number, int64(f.ContainerBytes),
			"corrupt frame data: checksum mismatch")
	}
	return nil
}

func (p *Pool) process(job Job) Outcome {
	f := job.Frame
	out := Outcome{Frame: f}
	if err := p.journal.Transition(f.Number, f.Track, journal.StateProcessing, ""); err != nil {
		out.Err = asStructured(err)
		_ = p.journal.Transition(f.Number, f.Track, journal.StateFailed, err.Error())
		return out
	}
	res, err := transform.Frame(f.Payload, job.Params)
	if err != nil {
		se := asStructured(err)
		_ = p.journal.Transition(f.Number, f.Track, journal.StateFailed, se.Error())
		out.Err = se
		return out
	}
	ts, err := clock.RTPTimestamp(f.CaptureNanos, clock.MediaClock)
	if err != nil {
		se := model.WrapError(model.StagePipeline, f.Number, int64(f.ContainerBytes), "time overflow", err)
		_ = p.journal.Transition(f.Number, f.Track, journal.StateFailed, se.Error())
		out.Err = se
		return out
	}
	o := &model.OutputFrame{
		Number:        f.Number,
		Track:         f.Track,
		Sequence:      f.Sequence,
		CaptureNanos:  f.CaptureNanos,
		RTPTimestamp:  ts,
		SampleRate:    job.Params.TargetRate,
		Channels:      job.Params.Target,
		TargetLUFS:    job.Params.TargetLUFS,
		MeasuredLUFS:  res.MeasuredLUFS,
		PayloadLength: uint32(len(res.PCM)),
		SHA256:        model.Checksum(res.PCM),
		Payload:       res.PCM,
	}
	if err = p.journal.Transition(f.Number, f.Track, journal.StateProcessed, ""); err != nil {
		out.Err = asStructured(err)
		return out
	}
	out.Output = o
	return out
}

func asStructured(err error) *model.StructuredError {
	if se, ok := model.AsStructured(err); ok {
		return se
	}
	return model.NewError(model.StagePipeline, 0, 0, err.Error())
}
