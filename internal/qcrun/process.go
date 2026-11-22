// Package qcrun orchestrates one pipeline execution: input digest ->
// digest-bound index -> unified-clock repair -> mapping validation ->
// bounded-queue streaming -> remux with rebuilt random-access index ->
// atomic commit. It also owns the checkpoint/resume decisions and the
// JSON summary consumed by the terminal front-end.
package qcrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"time"

	"mediaqc/internal/remux"
	"mediaqc/internal/stream"
	"mediaqc/internal/timeline"
)

// Options controls one pipeline run.
type Options struct {
	Input, Output string
	QueueCap      int
	ConsumerDelay time.Duration
	Resume        bool
	Mapping       map[uint64]uint64 // nil = identity
	MaxSkewNs     int64
	CancelAfter   int // test hook: cancel after N consumed packets
}

// Run executes scan -> repair -> validate -> stream -> remux and
// returns the JSON summary plus the process exit code.
func Run(ctx context.Context, opts Options) (remux.Summary, int) {
	sum := remux.Summary{Input: opts.Input, Output: opts.Output, ResumeAction: stream.ResumeFresh}
	fail := func(err error) (remux.Summary, int) {
		f := timeline.AsFault(err)
		sum.FromFault(f)
		return sum, f.Class.ExitCode()
	}

	in, err := os.Open(opts.Input)
	if err != nil {
		return fail(err)
	}
	defer in.Close()
	h := sha256.New()
	size, err := io.Copy(h, in)
	if err != nil {
		return fail(err)
	}
	var inputSHA [32]byte
	copy(inputSHA[:], h.Sum(nil))
	sum.InputSHA256 = hex.EncodeToString(inputSHA[:])

	// --- scanning stage: load or rebuild the digest-bound index ---
	idx, indexAction, err := loadOrScanIndex(opts.Input, in, size, inputSHA)
	if err != nil {
		return fail(err)
	}
	sum.IndexAction = indexAction
	sum.IndexChecksum = idx.Checksum
	sum.IndexEntries = len(idx.Keyframes())

	// --- resume decision: validate checkpoint before trusting it ---
	statePath := stream.StatePath(opts.Output)
	st, err := stream.LoadState(statePath)
	if err != nil {
		return fail(err)
	}
	sum.ResumeAction = stream.DecideResume(opts.Resume, st, inputSHA, idx.Checksum)
	if opts.Resume && st != nil && !st.Validate(inputSHA, idx.Checksum) {
		// Checkpoint failed validation: discard and restart from scan.
		idx, err = rescanIndex(opts.Input, in, size, inputSHA)
		if err != nil {
			return fail(err)
		}
		sum.IndexAction = "rescanned"
		sum.IndexChecksum = idx.Checksum
		sum.IndexEntries = len(idx.Keyframes())
	}

	// --- repair stage: unified-clock timestamp repair ---
	packets, durs, stats, err := repairPackets(idx, opts.MaxSkewNs)
	if err != nil {
		return fail(err)
	}
	sum.Repairs = remux.RepairCounts{
		Rebased:     stats.Rebased,
		Wraps:       stats.Wraps,
		Regressions: stats.Regressions,
	}
	sum.SyncErrorMs = timeline.NsToMs(stats.MaxSkewNs)

	// --- mapping validation: fail before any output file exists ---
	mapping := opts.Mapping
	if mapping == nil {
		mapping = map[uint64]uint64{}
		for _, t := range idx.Tracks {
			mapping[t.Number] = t.Number
		}
	}
	if err := remux.ValidateMapping(mapping, idx.Tracks); err != nil {
		return fail(err)
	}

	// --- checkpoint: scanned ---
	if err := stream.SaveState(statePath, &stream.State{
		InputSHA256:   sum.InputSHA256,
		IndexChecksum: idx.Checksum,
		Stage:         stream.StageScanned,
	}); err != nil {
		return fail(err)
	}

	// --- streaming + remux stage ---
	if err := streamAndRemux(ctx, opts, idx, packets, durs, mapping, &sum); err != nil {
		f := timeline.AsFault(err)
		os.Remove(opts.Output + ".tmp")
		if f.Class == timeline.ClassInterrupted {
			// Keep a resumable checkpoint; the half-written temp is gone.
			_ = stream.SaveState(statePath, &stream.State{
				InputSHA256:   sum.InputSHA256,
				IndexChecksum: idx.Checksum,
				Stage:         stream.StageInterrupted,
			})
			sum.FromFault(f)
			return sum, 3
		}
		return fail(err)
	}

	// --- RTP loss-recovery simulation over the streamed packets ---
	sum.PacketLossRecoveryRate = simulateRTPRecovery(packets)

	// --- commit: temp file + atomic rename ---
	outSHA, err := commitOutput(opts.Output)
	if err != nil {
		return fail(err)
	}
	sum.OutputSHA256 = outSHA
	os.Remove(statePath)
	return sum, 0
}
