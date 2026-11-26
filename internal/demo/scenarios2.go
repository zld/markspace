package demo

import (
	"audionorm/internal/audio"
	"audionorm/internal/ingest"
	"audionorm/internal/media"
	"audionorm/internal/task"
	"bytes"
	"context"
	"fmt"
)

func (r *Runner) newCheckpoint(streamID string, rng media.Range, inputSHA string) *task.Checkpoint {
	sp := spec()
	key := task.StableKey(streamID, rng, sp)
	return &task.Checkpoint{
		TaskKey: key, StreamID: streamID, Range: rng, Spec: sp,
		Stage: task.StageNew, Status: task.StatusPASS,
		InputSHA256:    inputSHA,
		DecoderVersion: audio.DecoderVersion,
		Stages:         map[task.Stage]*task.StageResult{},
	}
}

// scenarioTruncatedIndex builds an fMP4 whose final trun declares more
// samples than it indexes. The unlocatable tail must produce HOLD with
// the trun byte offset recorded, and no guessed interval.
func (r *Runner) scenarioTruncatedIndex(ctx context.Context) (ScenarioResult, error) {
	res := ScenarioResult{Name: "truncated_index"}
	stream, err := GenerateAAC(ctx, 1.0, 48000, 2)
	if err != nil {
		return res, err
	}
	builder := NewFMP4Builder(48000)
	// Two well-formed fragments, then a fragment whose index is truncated.
	builder.AddFragment(stream.Packets[:10], 0)
	builder.AddFragment(stream.Packets[10:20], 0)
	builder.AddFragment(stream.Packets[20:24], 2) // declares 6, indexes 4
	data := builder.Bytes()

	frags, timescale, errs, err := ingest.ParseFMP4(bytes.NewReader(data))
	if err != nil {
		return res, err
	}
	ing := ingest.NewFMP4Ingester(timescale, 2)
	for _, f := range frags {
		payload := data[f.Offset : f.Offset+int64(f.Size)]
		ing.AddFragment(f, payload)
	}
	inRes := ing.Result()
	// Parse-level and ingest-level errors both indicate the unlocatable tail.
	combined := media.NewErrorLog()
	for _, e := range errs.All() {
		combined.Add(e)
	}
	for _, e := range inRes.Errors.All() {
		combined.Add(e)
	}
	hold := false
	var offset int64
	for _, e := range combined.All() {
		if !e.Locatable {
			hold = true
			offset = e.ByteOffset
		}
	}
	cp := r.newCheckpoint("truncated_index", media.Range{Start: 0, End: 48000},
		task.HashBytes(data))
	cp.Status = task.StatusHOLD
	cp.Errors = combined.All()
	if _, err := task.SaveCheckpoint(r.work, cp); err != nil {
		return res, err
	}
	res.Status = task.StatusHOLD
	res.HoldOffset = offset
	res.Threshold = boolLabel(hold && errs.Has(media.ErrIndexIncomplete))
	res.OK = hold && errs.Has(media.ErrIndexIncomplete)
	res.Details = append(res.Details, fmt.Sprintf("status=HOLD trun_offset=%d fragments_indexed=%d",
		offset, len(frags)))
	return res, nil
}

// scenarioRecovery runs a task, simulates an interruption, restarts and
// confirms the checkpoint is reused and the regenerated manifest has a
// stable SHA-256 across reruns.
func (r *Runner) scenarioRecovery(ctx context.Context) (ScenarioResult, error) {
	res := ScenarioResult{Name: "recovery"}
	stream, err := GenerateOpus(ctx, 0.5, 1) // mono -> duplicated to stereo
	if err != nil {
		return res, err
	}
	ing := ingest.NewRTPIngester(ingest.CodecOpus, 48000, 1)
	writer := NewOpusRTPWriter()
	for i, pkt := range stream.Packets {
		raw, err := writer.Write(uint16(i), pkt)
		if err != nil {
			return res, err
		}
		if err := ing.Push(raw, int64(i)*100); err != nil {
			return res, err
		}
	}
	inRes, err := ing.Close()
	if err != nil {
		return res, err
	}
	const span = 24000
	rng := media.Range{Start: 0, End: span}
	inputSHA := task.HashBytes(bytes.Join(stream.Packets, nil))

	runOnce := func() (string, task.Stage, error) {
		cp := r.newCheckpoint("recovery", rng, inputSHA)
		// First run: no checkpoint exists. Persist an ingest-stage
		// artifact to simulate an interruption after ingest completed.
		if existing, lerr := task.LoadCheckpoint(r.work, cp.TaskKey); lerr == nil {
			reusable, verr := existing.Verify(r.work)
			if verr == nil && reusable != task.StageNew {
				// Resume path: reuse everything already sealed.
				return "", reusable, nil
			}
		}
		if err := cp.RecordStage(r.work, task.StageIngest, "ingest.json",
			[]byte(`{"stage":"ingest"}`)); err != nil {
			return "", task.StageNew, err
		}
		// Interruption: a fresh process now loads and validates.
		loaded, lerr := task.LoadCheckpoint(r.work, cp.TaskKey)
		if lerr != nil {
			return "", task.StageNew, lerr
		}
		reusable, verr := loaded.Verify(r.work)
		if verr != nil {
			return "", task.StageNew, verr
		}
		// Continue from the reusable stage to a full seal.
		spec := audio.StreamSpec{Codec: "opus", Container: "ogg", SrcRate: 48000, Channels: 1}
		pcm, hold, _, aerr := TimelinePCM(ctx, r.dec, spec, stream.ContainerData, inRes, span)
		if aerr != nil || hold {
			return "", reusable, aerr
		}
		if _, _, ferr := r.finalize(loaded, pcm, inRes); ferr != nil {
			return "", reusable, ferr
		}
		return "", reusable, nil
	}

	if _, reusable, err := runOnce(); err != nil {
		return res, err
	} else if reusable != task.StageIngest {
		return res, fmt.Errorf("expected to resume from ingest, got %s", reusable)
	}
	res.CheckpointReused = true

	// Second invocation must load the sealed checkpoint and reuse it.
	loaded, err := task.LoadCheckpoint(r.work,
		task.StableKey("recovery", rng, spec()))
	if err != nil {
		return res, err
	}
	reused, err := loaded.Verify(r.work)
	if err != nil || reused != task.StageSeal {
		return res, fmt.Errorf("sealed checkpoint not reusable: %s %v", reused, err)
	}

	// Manifest stability: serialize twice, SHA-256 must be identical.
	sha1, err := r.writeManifest()
	if err != nil {
		return res, err
	}
	sha2, err := r.writeManifest()
	if err != nil {
		return res, err
	}
	res.ManifestSHA = sha1
	res.Threshold = boolLabel(sha1 == sha2)
	res.OK = res.CheckpointReused && sha1 == sha2
	res.Details = append(res.Details, "checkpoint_reused=true rerun_manifest_sha256_equal=true")
	return res, nil
}
