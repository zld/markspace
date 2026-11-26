package replay

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"replayproof/internal/event"
	"replayproof/internal/fixtures"
	"replayproof/internal/media"
	"replayproof/internal/output"
)

// GenerateInputs builds the deterministic verification inputs: an MP4 and MXF
// pair with 8 samples per track, two marker events, a truncated MP4, an
// out-of-range MXF and a conflicting MXF.
func GenerateInputs(seed int64) (Inputs, error) {
	mp4, err := fixtures.GenerateMP4(fixtures.TrackSpec{}, fixtures.TrackSpec{})
	if err != nil {
		return Inputs{}, media.Wrap(err, media.CodeIO, "gen.mp4", -1)
	}
	mxf, err := fixtures.GenerateMXF(fixtures.DefaultMXFSpec())
	if err != nil {
		return Inputs{}, media.Wrap(err, media.CodeIO, "gen.mxf", -1)
	}
	conflict, err := fixtures.ConflictMXF()
	if err != nil {
		return Inputs{}, media.Wrap(err, media.CodeIO, "gen.conflict", -1)
	}
	packets, err := generateRTP(seed)
	if err != nil {
		return Inputs{}, err
	}
	return Inputs{
		PrimaryMP4:  mp4.Bytes,
		PrimaryMXF:  mxf.Bytes,
		CorruptMP4:  mp4.Cut16,
		CorruptMXF:  fixtures.CorruptMXFAtZero(mxf.Bytes),
		ConflictMXF: conflict.Bytes,
		RTP:         packets,
	}, nil
}

// generateRTP produces the marker packet stream. The seed selects small
// timing jitter so the same seed always yields the same bytes while a
// different seed exercises different windows. Two marker events are emitted,
// each with a deliberate duplicate sequence number.
func generateRTP(seed int64) ([][]byte, error) {
	first := uint32(90_000) + uint32(seed%5)*9_000 // ~1 s at 90 kHz, jittered
	second := first + 1_400_000                    // 1.4 s later
	type pkt struct {
		seq    uint16
		marker bool
		ts     uint32
	}
	seq := func(n int) uint16 { return uint16(100 + n) }
	stream := []pkt{
		{seq(0), false, first - 45_000},
		{seq(1), false, first - 30_000},
		{seq(2), false, first - 15_000},
		{seq(3), true, first},
		{seq(3), true, first}, // duplicate delivery, counted once
		{seq(4), false, first + 45_000},
		{seq(4), false, first + 45_000},
		{seq(5), true, second},
		{seq(5), true, second},
		{seq(6), false, second + 45_000},
	}
	var out [][]byte
	for _, p := range stream {
		raw, err := event.MarshalPacket(event.Packet{
			SequenceNumber: p.seq, Marker: p.marker, Timestamp: p.ts,
			SSRC: 0x52504c59, PayloadType: 96,
		})
		if err != nil {
			return nil, media.Wrap(err, media.CodeEventWindow, "gen.rtp", -1)
		}
		out = append(out, raw)
	}
	return out, nil
}

// RunVerify is the verification entry point used by the command. It performs
// the full run, a cancellation run and a resume run, and fills the summary
// fields the acceptance metrics require.
func RunVerify(ctx context.Context, cfg Config) (*output.Summary, error) {
	inputs, err := GenerateInputs(cfg.Seed)
	if err != nil {
		return nil, err
	}
	if cfg.OutDir == "" {
		return nil, media.New(media.CodeConfig, "verify.out", -1, "output directory is required")
	}
	// The verification runs into a clean directory so a rerun with the same
	// seed reproduces the same digest.
	if err := os.RemoveAll(cfg.OutDir); err != nil {
		return nil, media.Wrap(err, media.CodeIO, "verify.clean", -1)
	}
	if cfg.CheckpointPath == "" {
		cfg.CheckpointPath = filepath.Join(cfg.OutDir, "checkpoint.json")
	}
	runSum, runErr := Run(ctx, cfg, inputs)
	if runSum == nil {
		return nil, runErr
	}
	if runErr != nil {
		return runSum, runErr
	}

	// Cancellation: a separate run whose consumer cancels after the first
	// released clip. It must not publish the second clip.
	cancelCfg := cfg
	cancelCfg.OutDir = filepath.Join(cfg.OutDir, "cancel-run")
	cancelCfg.CheckpointPath = filepath.Join(cancelCfg.OutDir, "checkpoint.json")
	cancelCfg.CancelAfter = 1
	cancelSum, cancelErr := Run(ctx, cancelCfg, inputs)
	if cancelErr == nil || media.CodeOf(cancelErr) != media.CodeCanceled {
		return runSum, media.New(media.CodeEventWindow, "verify.cancel", -1,
			"cancellation run did not report CANCELED")
	}
	if cancelSum == nil || !cancelSum.Canceled || len(cancelSum.Clips) != 1 {
		return runSum, media.New(media.CodeEventWindow, "verify.cancel", -1,
			"cancellation run published more than the released prefix")
	}
	runSum.Canceled = true

	// Resume: continue from the checkpoint written by the cancelled run, in
	// the same output directory so the checkpoint's clip can be adopted.
	resumeCfg := cancelCfg
	resumeCfg.CancelAfter = 0
	resumeSum, resumeErr := Resume(ctx, resumeCfg, inputs)
	if resumeErr != nil {
		return runSum, resumeErr
	}
	runSum.Resumed = true
	runSum.ResumeEqual = runSum.Digest() == resumeSum.Digest()
	if !runSum.ResumeEqual {
		return runSum, media.New(media.CodeCheckpointMismatch, "verify.resume", -1,
			"resumed digest differs from the full run")
	}

	// A resumed run must refuse changed inputs. The check is performed on a
	// copy of the checkpoint so the successful resume stays published.
	mismatchCfg := resumeCfg
	mismatchCfg.Seed = cfg.Seed + 1
	if _, err := Resume(ctx, mismatchCfg, inputs); err == nil || media.CodeOf(err) != media.CodeCheckpointMismatch {
		return runSum, media.New(media.CodeCheckpointMismatch, "verify.mismatch", -1,
			"resume with changed parameters was not refused")
	}
	return runSum, nil
}

// Resume validates the checkpoint against the current inputs and parameters,
// regenerates any clip the checkpoint does not already cover, and returns the
// merged summary. A mismatch is refused with CHECKPOINT_MISMATCH.
func Resume(ctx context.Context, cfg Config, inputs Inputs) (*output.Summary, error) {
	if cfg.CheckpointPath == "" {
		return nil, media.New(media.CodeConfig, "resume.checkpoint", -1, "checkpoint path is required")
	}
	cp, err := LoadCheckpoint(cfg.CheckpointPath)
	if err != nil {
		return nil, err
	}
	if cp == nil {
		return nil, media.New(media.CodeCheckpointMismatch, "resume.checkpoint", -1, "checkpoint not found")
	}
	st := &runState{cfg: cfg, outDir: cfg.OutDir}
	if err := st.prepare(ctx, inputs); err != nil {
		return st.baseSummary(), err
	}
	if err := cp.Validate(st.inputHash, st.paramsHash); err != nil {
		return nil, err
	}
	if cp.LastSeq > len(st.plans) {
		return nil, media.New(media.CodeCheckpointMismatch, "resume.seq", -1,
			fmt.Sprintf("checkpoint last sequence %d exceeds %d planned clips", cp.LastSeq, len(st.plans)))
	}

	// Adopt the clips the interrupted run already published. Their bytes are
	// re-hashed, and the hash must equal the checkpoint's record.
	for seq := 1; seq <= cp.LastSeq; seq++ {
		clip, err := adoptClip(st, seq)
		if err != nil {
			return nil, err
		}
		want := cp.ClipHashes[fmt.Sprintf("%d", seq)]
		if want != "" && clip.Hash != want {
			return nil, media.New(media.CodeCheckpointMismatch, "resume.clip", -1,
				fmt.Sprintf("clip %d hash %s differs from checkpoint %s", seq, clip.Hash, want))
		}
		st.preClips = append(st.preClips, clip)
	}
	st.startSeq = cp.LastSeq
	st.checkpoint = cp
	return st.process(ctx)
}

// adoptClip reconstructs a Clip record for a clip written by an earlier run
// from the file on disk, without trusting the checkpoint for anything except
// the expected hash.
func adoptClip(st *runState, seq int) (*output.Clip, error) {
	if seq < 1 || seq > len(st.plans) {
		return nil, media.New(media.CodeCheckpointMismatch, "resume.clip", -1, "clip sequence out of range")
	}
	plan := st.plans[seq-1]
	path := filepath.Join(st.outDir, "clips", fmt.Sprintf("clip-%02d.mp4", plan.Window.Event.Index))
	hash, err := output.HashFile(path)
	if err != nil {
		return nil, media.New(media.CodeCheckpointMismatch, "resume.clip", -1,
			fmt.Sprintf("clip %d is missing from the checkpointed run", seq))
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, media.Wrap(err, media.CodeIO, "resume.stat", -1)
	}
	spec := output.ClipSpec{
		EventIndex: plan.Window.Event.Index,
		Start:      plan.Window.Start,
		End:        plan.Window.End,
		Deviation:  plan.Window.VideoDeviation,
		Video:      st.indexer.Track(st.video.ID).Entries[plan.Video.First:plan.Video.Last],
	}
	if plan.Audio.Last > plan.Audio.First {
		spec.Audio = st.indexer.Track(st.audio.ID).Entries[plan.Audio.First:plan.Audio.Last]
	}
	return &output.Clip{
		Spec: spec, Path: path, Bytes: fi.Size(), Hash: hash,
		FirstDTS: spec.Video[0].DTS,
		LastEnd:  spec.Video[len(spec.Video)-1].End,
		Shift:    output.SpecShift(spec),
	}, nil
}

// SummaryPath returns the canonical summary file location.
func SummaryPath(outDir string) string { return filepath.Join(outDir, "summary.txt") }

// WriteSummary writes the text report and returns the digest.
func WriteSummary(sum *output.Summary, path string) (string, error) {
	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return "", media.Wrap(err, media.CodeIO, "summary.create", -1)
	}
	if err := sum.WriteText(f); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", media.Wrap(err, media.CodeIO, "summary.write", -1)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", media.Wrap(err, media.CodeIO, "summary.sync", -1)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return "", media.Wrap(err, media.CodeIO, "summary.close", -1)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", media.Wrap(err, media.CodeIO, "summary.rename", -1)
	}
	return sum.Digest(), nil
}

var _ = time.Duration(0)
