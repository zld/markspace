// Package batch drives an offline, resumable remuxing run. It wires the
// demuxer, checkpoint store, muxer and digest together: blocks are committed
// in fixed-size batches, an interruption can be injected after N blocks, and a
// re-run resumes strictly after the last validated checkpoint rather than
// re-reading completed input.
package batch

import (
	"bytes"
	"crypto/sha256"
	"time"

	"mediapack/internal/checkpoint"
	"mediapack/internal/demux"
	"mediapack/internal/fault"
	"mediapack/internal/mux"
	"mediapack/internal/verify"
)

// Options configures one run.
type Options struct {
	InputPath string
	IndexDir  string
	BatchSize int
	// InterruptAfter, when > 0, stops the run after this many NEW blocks
	// (excluding any resumed blocks) to simulate a mid-batch failure.
	InterruptAfter int
	// CorruptIndex, when true, simulates a damaged index to exercise the
	// resume-refusal path.
	CorruptIndex bool
}

// ResumeInfo describes how a run started relative to a prior checkpoint.
type ResumeInfo struct {
	Resumed       bool
	ResumeBlock   int   // block number from which processing continued
	CheckpointOff int64 // committed input offset carried by the checkpoint
	Recoverable   bool
}

// Outcome is the complete result of a run, including timing and the digest.
type Outcome struct {
	Report *verify.Report
	Resume ResumeInfo
	// ElapsedMS is the wall-clock duration of this run.
	ElapsedMS int64
	// NewBlocks counts blocks actually processed after any checkpoint skip.
	// A resumed run has fewer of these than a full run, which is the
	// deterministic reason its elapsed time is lower.
	NewBlocks     int
	BlocksWritten int
	OutputPath    string
	// MuxResult is non-nil when remuxing produced a container.
	MuxResult *mux.Result
}

// Runner executes runs against a single input identity.
type Runner struct {
	opts Options
}

// New creates a runner.
func New(opts Options) *Runner {
	if opts.BatchSize <= 0 {
		opts.BatchSize = 4
	}
	return &Runner{opts: opts}
}

// runInput bundles the open input with its identity hash.
type runInput struct {
	data []byte
	sum  [32]byte
	size int64
}

// Execute performs one (possibly resuming) run over the given input bytes.
// The output WebM is written to outWriter; pass a fresh buffer on a clean run
// and the same buffer across a resume when appending is desired (the runner
// itself rebuilds the full container from the blocks it processes).
func (r *Runner) Execute(in *runInput, outWriter *bytes.Buffer) (*Outcome, error) {
	start := time.Now()
	out := &Outcome{}

	store, err := checkpoint.OpenStoreWithSize(r.opts.IndexDir, r.opts.BatchSize,
		r.opts.InputPath, in.sum, in.size)
	if err != nil {
		return nil, err
	}
	if r.opts.CorruptIndex {
		if err := r.simulateCorruptIndex(store); err != nil {
			return nil, err
		}
	}
	saved, err := store.Load()
	if err != nil {
		// Corrupt index / changed input: refuse to resume with a recoverable
		// fault that names the checkpoint offset.
		return out, err
	}
	if saved != nil {
		out.Resume = ResumeInfo{
			Resumed: true, ResumeBlock: saved.BlockCount,
			CheckpointOff: saved.CommittedBytes, Recoverable: true,
		}
	}

	d, err := demux.Open(bytes.NewReader(in.data))
	if err != nil {
		return out, err
	}

	acc := verify.NewAccumulator(d.Mapping())
	var packets []*demux.Packet
	var pendingHashes [][32]byte
	var pendingBytes int64
	totalBlocks := 0
	newBlocks := 0
	var streamErr error

	skip := 0
	if saved != nil {
		skip = saved.BlockCount
	}

	streamErr = d.Stream(func(p *demux.Packet) error {
		// Resume: blocks already covered by a validated checkpoint are not
		// reprocessed.
		if totalBlocks < skip {
			totalBlocks++
			return nil
		}
		totalBlocks++
		newBlocks++

		acc.Add(p)
		packets = append(packets, p)
		h := sha256.Sum256(p.Payload)
		pendingHashes = append(pendingHashes, h)
		pendingBytes = p.InputEnd

		// Commit at fixed batch boundaries.
		if len(pendingHashes) == r.opts.BatchSize {
			if _, cerr := store.Commit(pendingBytes, totalBlocks, pendingHashes); cerr != nil {
				return cerr
			}
			pendingHashes = nil
		}
		// Simulated interruption after N newly processed blocks.
		if r.opts.InterruptAfter > 0 && newBlocks >= r.opts.InterruptAfter {
			return errInterrupted
		}
		return nil
	})

	// A simulated interruption is not an input fault: commit whatever full
	// batches exist and report a partial, recoverable outcome.
	interrupted := streamErr == errInterrupted
	if streamErr != nil && !interrupted {
		out.Report = acc.Finalize(streamErr)
		out.ElapsedMS = time.Since(start).Milliseconds()
		return out, streamErr
	}
	// Flush a trailing partial batch so progress is durable.
	if len(pendingHashes) > 0 {
		if _, cerr := store.Commit(pendingBytes, totalBlocks, pendingHashes); cerr != nil {
			return out, cerr
		}
	}

	res, err := mux.WriteWebM(outWriter, d.Mapping(), packets)
	if err != nil {
		return out, err
	}
	out.MuxResult = res
	out.BlocksWritten = len(packets)
	out.NewBlocks = newBlocks
	out.Report = acc.Finalize(nil)
	out.ElapsedMS = time.Since(start).Milliseconds()
	if interrupted {
		// Surface the interruption through a recoverable marker so callers
		// can distinguish a deliberately cut run from completion.
		out.Report.Recoverable = true
	}
	return out, nil
}

// errInterrupted marks the injected stop; it never reaches the user as an
// input fault.
var errInterrupted = fault.New(fault.KindIO, -1, "simulated interruption")

// simulateCorruptIndex writes a syntactically valid but checksum-broken index.
func (r *Runner) simulateCorruptIndex(store *checkpoint.Store) error {
	garbage := []byte(`{"version":1,"block_count":3,"self_sha256":"deadbeef"}`)
	return writeFile(store.Path(), garbage)
}
