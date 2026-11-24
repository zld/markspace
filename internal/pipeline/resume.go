package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"medpipe/internal/clock"
)

// LoadResume validates input digest and parser major before restoring state.
// On validation failure it atomically retains a machine-readable reason file
// and returns a ResumeFailure suitable for CLI exit code 1.
func LoadResume(opts Options) (*Runner, *clock.Checkpoint, error) {
	cp, err := clock.LoadCheckpoint(checkpointPath(opts.OutDir))
	if err != nil {
		return nil, nil, &ResumeFailure{Reason: clock.Errorf(clock.CodeCheckpointInvalid, nil, "%s", err.Error())}
	}
	digest, err := HashFile(sourcePath(opts.OutDir))
	if err != nil {
		reason := clock.Wrap(clock.CodeInputDigestMismatch, nil, err, "cannot hash input source for resume")
		_ = clock.AtomicWriteJSON(rejectionPath(opts.OutDir), reason)
		return nil, cp, &ResumeFailure{Reason: reason}
	}
	if reason := cp.ValidateResume(digest, ParserMajor); reason != nil {
		_ = clock.AtomicWriteJSON(rejectionPath(opts.OutDir), reason)
		return nil, cp, &ResumeFailure{Reason: reason}
	}

	var st State
	if len(cp.State) > 0 {
		if err := json.Unmarshal(cp.State, &st); err != nil {
			reason := clock.Wrap(clock.CodeCheckpointInvalid, nil, err, "decode checkpoint pipeline state")
			_ = clock.AtomicWriteJSON(rejectionPath(opts.OutDir), reason)
			return nil, cp, &ResumeFailure{Reason: reason}
		}
	}
	cfg := unmarshalGeneratorConfig(cp.GeneratorConfig)
	r := NewRunner(opts, cfg, digest)
	st.ensureSlices()
	r.state = st
	r.tl.Restore(st.Timeline)
	r.reorder.Restore(st.Reorder)
	r.segmenter.Restore(st.Segmenter)
	r.silence.Restore(st.Silence)
	r.deduper.Restore(st.Deduper)
	return r, cp, nil
}

// WriteRejection is used by CLI-level version/argument checks as well.
func WriteRejection(outDir string, reason *clock.Error) {
	if outDir == "" || reason == nil {
		return
	}
	_ = os.MkdirAll(outDir, 0o755)
	_ = clock.AtomicWriteJSON(rejectionPath(outDir), reason)
}

// Resume validates and executes a checkpointed run. Completed checkpoints are
// verified from disk and return immediately, which keeps the demo resume path
// well below the required time bound.
func Resume(ctx context.Context, opts Options) (*Result, error) {
	started := time.Now()
	r, cp, err := LoadResume(opts)
	if err != nil {
		return nil, err
	}

	var res *Result
	if cp.Completed && cp.OutputSHA256 != "" {
		manifestSHA, outputSHA, verr := VerifyChecksumFile(opts.OutDir)
		if verr != nil {
			reason := clock.Wrap(clock.CodeCheckpointInvalid, nil, verr, "completed output verification failed")
			WriteRejection(opts.OutDir, reason)
			return nil, &ResumeFailure{Reason: reason}
		}
		if outputSHA != cp.OutputSHA256 {
			reason := clock.Errorf(clock.CodeCheckpointInvalid, nil,
				"output checksum %s does not match checkpoint %s", outputSHA, cp.OutputSHA256)
			WriteRejection(opts.OutDir, reason)
			return nil, &ResumeFailure{Reason: reason}
		}
		res = r.buildResult(manifestSHA, outputSHA)
		res.Status = "resume_verified_completed"
		// A successful resume/verification is exit 0 even when the immutable
		// source contains a previously located corruption.
		res.ExitCode = 0
	} else {
		res, err = r.Run(ctx)
		if err != nil && !errors.Is(err, ErrInterrupted) {
			return res, err
		}
		if errors.Is(err, ErrInterrupted) {
			res.ResumeMS = time.Since(started).Milliseconds()
			res.ResumeFromSeq = cp.LastSequence
			res.ResumeFromExtSeq = cp.LastExtSequence
			return res, err
		}
		res.Status = "resumed_and_completed"
	}

	res.ResumeMS = time.Since(started).Milliseconds()
	res.ResumeFromSeq = cp.LastSequence
	res.ResumeFromExtSeq = cp.LastExtSequence
	return res, nil
}
