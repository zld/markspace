package pipeline

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"mediad/internal/checkpoint"
)

// commit persists the boundary after one fully validated audio block.
func (r *Runner) commit() error {
	if r.ckptPath == "" {
		return nil
	}
	prefix := r.prefixBuf.Bytes()
	loss := r.window.Loss()
	st := &checkpoint.State{
		JobID:           r.cfg.InputPath,
		InputPath:       r.cfg.InputPath,
		ConfigSummary:   checkpoint.SummaryHash(r.cfg.ConfigJSON()),
		PrefixHash:      checkpoint.PrefixHashOf(prefix),
		CommittedPrefix: append([]byte(nil), prefix...),
		ConsumedOffset:  int64(len(prefix)),
		NextSeq:        r.window.NextSeq(),
		BlocksDone:     r.blocks,
		OutputSamples:  r.samplesOut,
		OutputHash:     r.SHA256(),
		Packets:        r.packets,
		Duplicates:     r.window.Stats().Duplicates,
		Reordered:      r.window.Stats().Reordered,
		LateDropped:    r.window.Stats().LateDropped,
		Gaps:           loss.GapCount(),
		Bursts:         loss.Bursts(),
		SamplesIn:      r.samplesIn,
		Clipped:        r.clipped,
		PeakDBFS:       r.peak,
		ClipBefore:     r.clipBefore,
		ClipAfter:      r.clipAfter,
	}
	r.state = st
	return checkpoint.Save(r.ckptPath, st)
}

// SHA256 exposes the running output hash, also used by the final report.
func (r *Runner) SHA256() string {
	return fmt.Sprintf("%x", r.hasher.Sum(nil))
}

// Resume opens the input and checkpoint, validates the boundary and, on
// success, returns a runner positioned after the last committed block plus a
// reader over only the unconsumed suffix. out must be the existing output
// file opened for append; its committed prefix is folded back into the
// running hash so the final checksum equals an uninterrupted run. On
// mismatch it returns a checkpoint error mapped by the CLI to exit code 3.
func Resume(cfg Config, inPath, ckptPath string, out *os.File) (*Runner, io.Reader, int64, error) {
	start := time.Now()
	st, err := checkpoint.Load(ckptPath)
	if err != nil {
		return nil, nil, 0, err
	}
	raw, err := os.ReadFile(inPath)
	if err != nil {
		return nil, nil, 0, err
	}
	if st.ConsumedOffset > int64(len(raw)) {
		return nil, nil, 0, &checkpoint.MismatchError{
			Reason: checkpoint.ErrPrefixMismatch, Offset: int64(len(raw)),
		}
	}
	livePrefix := raw[:st.ConsumedOffset]
	// The stored committed prefix enables a true byte-level diff that reports
	// the exact divergence offset; the hashes are the tamper proof.
	if err := st.Verify(cfg.ConfigJSON(), st.CommittedPrefix, livePrefix); err != nil {
		return nil, nil, 0, err
	}

	// The output must already contain exactly the committed sample prefix.
	info, err := out.Stat()
	if err != nil {
		return nil, nil, 0, err
	}
	wantBytes := st.OutputSamples * 2
	if info.Size() != wantBytes {
		return nil, nil, 0, &checkpoint.MismatchError{
			Reason: checkpoint.ErrPrefixMismatch, Offset: info.Size(),
		}
	}
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		return nil, nil, 0, err
	}

	r, err := NewRunner(cfg, out, ckptPath, 0)
	if err != nil {
		return nil, nil, 0, err
	}
	// Re-hash the existing output prefix, then move the append cursor to EOF.
	if _, err := io.Copy(r.hasher, io.LimitReader(out, wantBytes)); err != nil {
		return nil, nil, 0, err
	}
	if _, err := out.Seek(0, io.SeekEnd); err != nil {
		return nil, nil, 0, err
	}

	r.blocks = st.BlocksDone
	r.samplesOut = st.OutputSamples
	r.basePackets = st.Packets
	r.baseDup = st.Duplicates
	r.baseReorder = st.Reordered
	r.baseLate = st.LateDropped
	r.baseGaps = st.Gaps
	r.baseBursts = st.Bursts
	r.baseClipped = st.Clipped
	r.baseSamplesIn = st.SamplesIn
	r.basePeak = st.PeakDBFS
	r.baseClipBefore = st.ClipBefore
	r.baseClipAfter = st.ClipAfter
	r.peak = st.PeakDBFS
	r.clipBefore = st.ClipBefore
	r.clipAfter = st.ClipAfter
	r.clipped = st.Clipped
	r.samplesIn = st.SamplesIn
	r.packets = 0
	// Seed the committed input prefix so later checkpoints stay comparable.
	r.prefixBuf.Write(raw[:st.ConsumedOffset])
	r.state = st
	r.resumeMS = time.Since(start).Milliseconds()
	return r, bytes.NewReader(raw[st.ConsumedOffset:]), st.ConsumedOffset, nil
}

// IsCheckpointMismatch reports whether err is a checkpoint validation failure.
func IsCheckpointMismatch(err error) bool {
	var me *checkpoint.MismatchError
	return errors.As(err, &me)
}
