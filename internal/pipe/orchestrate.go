package pipe

import (
	"context"
	"path/filepath"
	"sort"
	"sync"

	"mediacore/internal/checkpoint"
	"mediacore/internal/diagnostic"
	"mediacore/internal/index"
	"mediacore/internal/manifest"
	"mediacore/internal/provenance"
)

const DefaultMemoryBytes int64 = 64 << 20

type Config struct {
	WorkDir        string `json:"-"`
	MaxReaders     int    `json:"max_readers"`
	MaxBufferBytes int64  `json:"max_buffer_bytes"`
	QueueCapacity  int    `json:"queue_capacity"`
	ChunkBytes     int    `json:"chunk_bytes"`
}

func DefaultConfig(workDir string) Config {
	return Config{
		WorkDir:        workDir,
		MaxReaders:     2,
		MaxBufferBytes: DefaultMemoryBytes,
		QueueCapacity:  4,
		ChunkBytes:     64 * 1024,
	}
}

type ItemOutcome struct {
	Entry          manifest.Entry     `json:"entry"`
	Index          *index.EntryIndex  `json:"index,omitempty"`
	Range          *index.RangeReport `json:"range,omitempty"`
	Record         *provenance.Record `json:"record,omitempty"`
	IndexPath      string             `json:"index_path,omitempty"`
	ProvenancePath string             `json:"provenance_path,omitempty"`
	ReportPath     string             `json:"report_path,omitempty"`
	Skipped        bool               `json:"skipped"`
	AlignmentErr   error              `json:"-"`
	Err            error              `json:"-"`
}

func (o *ItemOutcome) FailurePosition() diagnostic.Position {
	if o.Err != nil {
		return diagnostic.PositionOf(o.Err)
	}
	if o.AlignmentErr != nil {
		return diagnostic.PositionOf(o.AlignmentErr)
	}
	return diagnostic.Position{}
}

type RunReport struct {
	InputDigest     string         `json:"input_digest"`
	ConfigDigest    string         `json:"config_digest"`
	CheckpointID    string         `json:"checkpoint_id"`
	CheckpointPath  string         `json:"checkpoint_path"`
	Outcomes        []*ItemOutcome `json:"outcomes"`
	Completed       []int          `json:"completed_entry_numbers"`
	BufferPeak      int64          `json:"buffer_peak_bytes"`
	JobQueuePeak    int            `json:"job_queue_peak"`
	ResultQueuePeak int            `json:"result_queue_peak"`
}

// ProcessEntries runs bounded scan workers, persists each successful item, and
// advances the checkpoint only after both index and provenance writes succeed.
func ProcessEntries(ctx context.Context, entries []manifest.Entry, inputDigest string, cfg Config) (*RunReport, error) {
	for i := range entries {
		entries[i].EntryNumber = i
	}
	if cfg.MaxReaders <= 0 {
		cfg.MaxReaders = 2
	}
	if cfg.MaxBufferBytes <= 0 {
		cfg.MaxBufferBytes = DefaultMemoryBytes
	}
	if cfg.QueueCapacity <= 0 {
		cfg.QueueCapacity = cfg.MaxReaders * 2
	}
	if cfg.ChunkBytes <= 0 || int64(cfg.ChunkBytes) > cfg.MaxBufferBytes {
		cfg.ChunkBytes = int(min64(64*1024, cfg.MaxBufferBytes))
	}
	configDigest, err := provenance.ConfigDigest(map[string]any{
		"max_readers":      cfg.MaxReaders,
		"max_buffer_bytes": cfg.MaxBufferBytes,
		"chunk_bytes":      cfg.ChunkBytes,
		"mapping_version":  index.MappingRuleVersion,
	})
	if err != nil {
		return nil, err
	}
	cpPath := checkpoint.Path(cfg.WorkDir)
	cp, err := checkpoint.Load(cpPath, inputDigest, configDigest)
	report := &RunReport{
		InputDigest:    inputDigest,
		ConfigDigest:   configDigest,
		CheckpointID:   checkpoint.Identity(inputDigest, configDigest),
		CheckpointPath: cpPath,
	}
	if err != nil {
		return report, err
	}

	jobs := NewQueue[manifest.Entry](cfg.QueueCapacity)
	results := NewQueue[*ItemOutcome](cfg.QueueCapacity)
	budget := NewByteBudget(cfg.MaxBufferBytes)
	doneStates := make(map[string]checkpoint.EntryState, len(cp.Entries))
	for k, v := range cp.Entries {
		doneStates[k] = v
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	for w := 0; w < cfg.MaxReaders; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				e, ok, qerr := jobs.Recv(ctx)
				if qerr != nil || !ok {
					return
				}
				out := processOne(ctx, e, cfg, budget, doneStates, inputDigest, configDigest)
				// The collector always drains results; use Background so a terminal
				// located error is not lost during cancellation.
				if serr := results.Send(context.Background(), out); serr != nil {
					return
				}
				if out.Err != nil {
					cancel()
				}
			}
		}()
	}
	go func() {
		for _, e := range entries {
			if err := jobs.Send(ctx, e); err != nil {
				break
			}
		}
		jobs.Close()
	}()
	go func() {
		wg.Wait()
		results.Close()
	}()

	var firstErr error
	for out := range results.Channel() {
		report.Outcomes = append(report.Outcomes, out)
		if out.Err != nil {
			if firstErr == nil {
				firstErr = out.Err
			}
			cancel()
			continue
		}
		if out.Skipped {
			continue
		}
		if err := commitOutcome(cfg.WorkDir, cpPath, cp, inputDigest, configDigest, out); err != nil && firstErr == nil {
			out.Err = err
			firstErr = err
			cancel()
		}
	}
	sort.Slice(report.Outcomes, func(i, j int) bool {
		return report.Outcomes[i].Entry.EntryIndex < report.Outcomes[j].Entry.EntryIndex
	})
	if firstErr == nil && ctx.Err() != nil {
		firstErr = diagnostic.Wrap(diagnostic.ErrCancelled, diagnostic.Location{Stage: "pipe", ByteOffset: -1}, ctx.Err())
	}
	report.Completed = cp.CompletedNumbers()
	report.BufferPeak = budget.Peak()
	report.JobQueuePeak = jobs.Peak()
	report.ResultQueuePeak = results.Peak()
	return report, firstErr
}

func processOne(ctx context.Context, e manifest.Entry, cfg Config, budget *ByteBudget, done map[string]checkpoint.EntryState, inputDigest, configDigest string) *ItemOutcome {
	out := &ItemOutcome{Entry: e}
	key := provenance.FileKey(e.StableKey)
	out.IndexPath = filepath.Join(cfg.WorkDir, "indexes", key+".json")
	out.ProvenancePath = filepath.Join(cfg.WorkDir, "provenance", key+".json")
	out.ReportPath = filepath.Join(cfg.WorkDir, "reports", key+".json")

	if state, isDone := done[e.StableKey]; isDone {
		out.Skipped = true
		out.IndexPath = state.IndexPath
		out.ProvenancePath = state.ProvenancePath
		idx := &index.EntryIndex{}
		if err := provenance.ReadJSON(state.IndexPath, idx); err != nil {
			out.Err = diagnostic.Wrap(nil, e.Location("recovery"), err)
			return out
		}
		rec := &provenance.Record{}
		if err := provenance.ReadJSON(state.ProvenancePath, rec); err != nil {
			out.Err = diagnostic.Wrap(nil, e.Location("recovery"), err)
			return out
		}
		out.Index = idx
		out.Record = rec
		if idx.Aligned {
			r, qerr := index.QueryIndex(idx, e.ProgramStartU, e.ProgramEndU)
			if qerr != nil {
				out.Err = qerr
			}
			out.Range = r
		} else {
			out.AlignmentErr = diagnostic.New(diagnostic.ErrMisaligned, e.Location("query"), "representation is not precisely aligned")
		}
		return out
	}

	chunk := int64(cfg.ChunkBytes)
	if err := budget.Acquire(ctx, chunk); err != nil {
		out.Err = err
		return out
	}
	defer budget.Release(chunk)
	opts := index.DefaultScanOptions()
	opts.ChunkBytes = cfg.ChunkBytes
	idx, err := index.ScanEntry(e, opts)
	if err != nil {
		out.Err = err
		return out
	}
	out.Index = idx
	rec := provenance.NewRecord(inputDigest, configDigest, idx)
	out.Record = &rec
	if idx.Aligned {
		r, qerr := index.QueryIndex(idx, e.ProgramStartU, e.ProgramEndU)
		if qerr != nil {
			out.Err = qerr
			return out
		}
		out.Range = r
	} else {
		out.AlignmentErr = diagnostic.New(diagnostic.ErrMisaligned, e.Location("query"), "representation deviates by more than 20ms")
	}
	return out
}

func commitOutcome(workDir, cpPath string, cp *checkpoint.Checkpoint, inputDigest, configDigest string, out *ItemOutcome) error {
	if out.Index == nil || out.Record == nil {
		return diagnostic.New(diagnostic.ErrCheckpoint, out.Entry.Location("pipe"), "refusing checkpoint without index and provenance")
	}
	if err := provenance.WriteAtomicJSON(out.IndexPath, out.Index); err != nil {
		return diagnostic.Wrap(nil, out.Entry.Location("persist"), err)
	}
	if err := provenance.WriteAtomicJSON(out.ProvenancePath, out.Record); err != nil {
		return diagnostic.Wrap(nil, out.Entry.Location("persist"), err)
	}
	if out.Range != nil {
		_ = provenance.WriteAtomicJSON(out.ReportPath, out.Range)
	}
	cp.Mark(checkpoint.EntryState{
		EntryNumber:    out.Entry.EntryNumber,
		StableKey:      out.Entry.StableKey,
		IndexPath:      out.IndexPath,
		ProvenancePath: out.ProvenancePath,
		IndexDigest:    out.Index.Digest,
	})
	if err := cp.Save(cpPath); err != nil {
		return err
	}
	return nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
