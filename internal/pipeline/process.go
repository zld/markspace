package pipeline

import (
	"context"
	"fmt"
	"io"

	"mediaroute/internal/index"
	"mediaroute/internal/ingest"
	"mediaroute/internal/model"
	"mediaroute/internal/timeline"
)

// Processor runs the three collaborating subsystems over one input stream:
// ingest/validation, timeline repair, then index/delivery. It keeps the
// command entry thin and lets tests exercise each phase independently.
type Processor struct {
	Cfg Config
}

// NewProcessor builds a Processor with validated bounds.
func NewProcessor(cfg Config) (*Processor, error) {
	norm, err := cfg.Normalize()
	if err != nil {
		return nil, err
	}
	return &Processor{Cfg: norm}, nil
}

// Outcome is the full result of processing one input.
type Outcome struct {
	Source  *model.SourceSet
	Repairs []model.RepairRecord
	Index   *model.Index
	Output  []byte
	SHA256  string
	Counts  Counts
}

// Counts are the shared counters reported to the metrics stage.
type Counts struct {
	MFHDGaps     int
	Duplicates   int
	RepairedTfdt int
	Truncated    int
	Dropped      int
	Recovered    int
}

// Process repairs raw input and renders a repaired fMP4 into out. Fragments
// flow through a bounded repair stage so backpressure and cancellation behave
// like the production pipeline. When out is nil the repaired bytes are
// returned in Outcome.Output.
func (p *Processor) Process(ctx context.Context, raw []byte, out io.Writer) (*Outcome, error) {
	if err := ctx.Err(); err != nil {
		return nil, model.NewTaskError(model.CategoryCanceled, "pipeline", "context cancelled before processing")
	}
	set, err := ingest.Read(raw, ingest.Config{
		MaxBoxSize: uint64(p.Cfg.MaxBoxBytes),
		Name:       "input",
	})
	if err != nil {
		return nil, err
	}
	rep, err := timeline.NewRepairer(set)
	if err != nil {
		return nil, err
	}
	if err := p.repairStage(ctx, set, rep); err != nil {
		return nil, err
	}
	res := rep.Result()

	var buf bytesBuffer
	dst := io.Writer(&buf)
	if out != nil {
		dst = out
	}
	delivery, err := index.WriteRepairedSet(dst, set, index.WriteOptions{})
	if err != nil {
		return nil, err
	}
	// delivery.Sha256 hashes exactly the bytes written, whether they went to
	// an external writer or the in-memory buffer.
	return &Outcome{
		Source:  set,
		Repairs: res.Repairs,
		Index:   delivery.Index,
		Output:  buf.data,
		SHA256:  delivery.Sha256,
		Counts: Counts{
			MFHDGaps:     res.MFHDGaps,
			Duplicates:   res.Duplicates,
			RepairedTfdt: res.RepairedTfdt,
			Truncated:    truncationCount(set),
			Dropped:      droppedCount(set),
			Recovered:    recoveredCount(set),
		},
	}, nil
}

// repairStage pushes every fragment through the bounded Run pipeline and
// copies repaired fragments back into the source set in source order. Repair
// must stay strictly ordered per track, so the pipeline carries the fragment
// index alongside the payload and the collector restores order.
func (p *Processor) repairStage(ctx context.Context, set *model.SourceSet, rep *timeline.Repairer) error {
	if len(set.Fragments) == 0 {
		return nil
	}
	// The repair stage is sequential by construction: mfhd and tfdt ordering
	// depend on the previous fragment. Backpressure still applies because the
	// stage writes into a bounded channel.
	stage := Stage{
		Name: "repair",
		Run: func(ctx context.Context, in <-chan model.Fragment, out chan<- model.Fragment) error {
			for {
				select {
				case <-ctx.Done():
					return model.NewTaskError(model.CategoryCanceled, "pipeline", "repair cancelled")
				case frag, ok := <-in:
					if !ok {
						return nil
					}
					cp := frag
					if err := rep.Apply(&cp); err != nil {
						return err
					}
					select {
					case out <- cp:
					case <-ctx.Done():
						return model.NewTaskError(model.CategoryCanceled, "pipeline", "repair cancelled while emitting")
					}
				}
			}
		},
	}
	repaired, err := Run(ctx, p.Cfg, set.Fragments, stage)
	if err != nil {
		return err
	}
	if len(repaired) != len(set.Fragments) {
		return model.NewTaskError(model.CategoryDownstream, "pipeline",
			fmt.Sprintf("repair stage returned %d of %d fragments", len(repaired), len(set.Fragments)))
	}
	set.Fragments = repaired
	return nil
}

// bytesBuffer accumulates the repaired file when the caller passes no writer.
type bytesBuffer struct{ data []byte }

func (b *bytesBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	return len(p), nil
}

func truncationCount(set *model.SourceSet) int {
	n := 0
	for i := range set.Fragments {
		if set.Fragments[i].Truncated {
			n++
		}
	}
	return n
}

func droppedCount(set *model.SourceSet) int {
	n := 0
	for i := range set.Fragments {
		if set.Fragments[i].Discarded {
			n++
		}
	}
	return n
}

func recoveredCount(set *model.SourceSet) int {
	n := 0
	for i := range set.Fragments {
		if set.Fragments[i].Recovered && !set.Fragments[i].Discarded {
			n++
		}
	}
	return n
}
