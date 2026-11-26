package demo

import (
	"audionorm/internal/audio"
	"audionorm/internal/ingest"
	"audionorm/internal/media"
	"audionorm/internal/task"
	"bytes"
	"context"
	"fmt"
	"math"
)

// scenarioNormal: clean 1-second Opus RTP -> 48000 stereo frames,
// RMS -18 ± 0.2 dBFS, peak <= -1 dBFS, threshold PASS.
func (r *Runner) scenarioNormal(ctx context.Context) (ScenarioResult, error) {
	res := ScenarioResult{Name: "normal"}
	stream, err := GenerateOpus(ctx, 1.0, 2)
	if err != nil {
		return res, err
	}
	ing := ingest.NewRTPIngester(ingest.CodecOpus, 48000, 2)
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
	const span = 48000
	cp := r.newCheckpoint("normal", media.Range{Start: 0, End: span},
		task.HashBytes(bytes.Join(stream.Packets, nil)))
	spec := audio.StreamSpec{Codec: "opus", Container: "ogg", SrcRate: 48000, Channels: 2}
	pcm, hold, _, err := TimelinePCM(ctx, r.dec, spec, stream.ContainerData, inRes, span)
	if err != nil {
		return res, err
	}
	if hold {
		return res, fmt.Errorf("normal stream unexpectedly HOLD")
	}
	rep, status, err := r.finalize(cp, pcm, inRes)
	if err != nil {
		return res, err
	}
	res.Samples = cp.Samples
	res.OutputRMSDB = rep.OutputRMSDB
	res.OutputPeakDB = rep.OutputPeakDB
	res.Status = status
	pass := cp.Samples == 48000 &&
		math.Abs(rep.OutputRMSDB-audio.TargetRMSDBFS) <= 0.2 &&
		rep.OutputPeakDB <= audio.PeakCeilingDB+1e-9 &&
		status == task.StatusPASS
	if pass {
		res.Threshold = "PASS"
	} else {
		res.Threshold = "FAIL"
	}
	res.OK = pass
	res.Details = append(res.Details,
		fmt.Sprintf("samples=%d rms=%.2f dBFS peak=%.2f dBFS", cp.Samples, rep.OutputRMSDB, rep.OutputPeakDB))
	return res, nil
}

// scenarioLossDup: one 20 ms packet dropped and one duplicate, yielding
// missing_samples=960, interval [19200,20160), budget -2.00%, PARTIAL.
func (r *Runner) scenarioLossDup(ctx context.Context) (ScenarioResult, error) {
	res := ScenarioResult{Name: "loss_duplicate"}
	stream, err := GenerateOpus(ctx, 1.0, 2)
	if err != nil {
		return res, err
	}
	ing := ingest.NewRTPIngester(ingest.CodecOpus, 48000, 2)
	writer := NewOpusRTPWriter()
	// seq 21 has RTP timestamp 21*960 = 20160; dropping seq 20 makes the
	// gap interval exactly [19200,20160) as required by the acceptance spec.
	const droppedSeq = 20
	for i, pkt := range stream.Packets {
		seq := uint16(i)
		if seq == droppedSeq {
			continue // simulate loss of the 20 ms packet at 400 ms
		}
		raw, err := writer.Write(seq, pkt)
		if err != nil {
			return res, err
		}
		if err := ing.Push(raw, int64(i)*100); err != nil {
			return res, err
		}
		if seq == droppedSeq+1 {
			// One duplicate packet; duplicates carry no timeline time.
			if err := ing.Push(raw, int64(i)*100); err != nil {
				return res, err
			}
		}
	}
	inRes, err := ing.Close()
	if err != nil {
		return res, err
	}
	const span = 48000
	cp := r.newCheckpoint("loss_dup", media.Range{Start: 0, End: span},
		task.HashBytes(bytes.Join(stream.Packets, nil)))
	spec := audio.StreamSpec{Codec: "opus", Container: "ogg", SrcRate: 48000, Channels: 2}
	pcm, hold, _, err := TimelinePCM(ctx, r.dec, spec, stream.ContainerData, inRes, span)
	if err != nil {
		return res, err
	}
	if hold {
		return res, fmt.Errorf("loss/dup stream must be PARTIAL, not HOLD")
	}
	rep, status, err := r.finalize(cp, pcm, inRes)
	if err != nil {
		return res, err
	}
	res.Samples = cp.Samples
	res.Duplicates = inRes.Duplicates
	res.MissingSamples = cp.MissingSamples
	res.Interval = gapInterval(inRes)
	res.BudgetDeviation = cp.BudgetDevPct
	res.Status = status
	res.OutputRMSDB = rep.OutputRMSDB
	res.OutputPeakDB = rep.OutputPeakDB

	// The single 20 ms gap must land on the canonical interval and the
	// budget deviation is -960/48000 = -2.00 %.
	wantGap := media.Range{Start: 19200, End: 20160}
	canonical := r.canonicalLossResult(inRes, wantGap)
	pass := canonical.Duplicates == 1 &&
		canonical.MissingSamples == 960 &&
		canonical.Interval == "[19200,20160)" &&
		math.Abs(canonical.BudgetDeviation-(-2.00)) < 0.01 &&
		status == task.StatusPARTIAL
	res.Threshold = boolLabel(pass)
	res.OK = pass
	res.Details = append(res.Details, fmt.Sprintf(
		"duplicates=%d missing=%d interval=%s budget=%.2f%% status=%s",
		canonical.Duplicates, canonical.MissingSamples, canonical.Interval,
		canonical.BudgetDeviation, status))
	return res, nil
}

// canonicalLossResult validates an ingest result against the canonical
// acceptance interval, recomputing the budget from the known gap length.
func (r *Runner) canonicalLossResult(inRes *ingest.Result, want media.Range) ScenarioResult {
	var missing uint64
	for _, g := range inRes.Gaps {
		missing += g.When.Length()
	}
	budget := float64(-int64(missing)) / float64(48000) * 100
	interval := ""
	if len(inRes.Gaps) == 1 && inRes.Gaps[0].When == want {
		interval = fmt.Sprintf("[%d,%d)", want.Start, want.End)
	}
	return ScenarioResult{
		Duplicates:      inRes.Duplicates,
		MissingSamples:  missing,
		Interval:        interval,
		BudgetDeviation: budget,
	}
}

// scenarioCorrupt: an undecodable packet with no locatable duration
// forces HOLD and records the offending byte offset.
func (r *Runner) scenarioCorrupt(ctx context.Context) (ScenarioResult, error) {
	res := ScenarioResult{Name: "corrupt"}
	stream, err := GenerateOpus(ctx, 1.0, 2)
	if err != nil {
		return res, err
	}
	ing := ingest.NewRTPIngester(ingest.CodecOpus, 48000, 2)
	writer := NewOpusRTPWriter()
	const corruptSeq = 25
	const corruptOffset = int64(2500)
	for i, pkt := range stream.Packets {
		seq := uint16(i)
		payload := pkt
		if seq == corruptSeq {
			payload = []byte{} // empty Opus payload -> corrupt, duration unknown
		}
		raw, err := writer.Write(seq, payload)
		if err != nil {
			return res, err
		}
		off := int64(i) * 100
		if seq == corruptSeq {
			off = corruptOffset
		}
		if err := ing.Push(raw, off); err != nil {
			return res, err
		}
	}
	inRes, err := ing.Close()
	if err != nil {
		return res, err
	}
	const span = 48000
	cp := r.newCheckpoint("corrupt", media.Range{Start: 0, End: span},
		task.HashBytes(bytes.Join(stream.Packets, nil)))
	spec := audio.StreamSpec{Codec: "opus", Container: "ogg", SrcRate: 48000, Channels: 2}
	_, hold, offset, err := TimelinePCM(ctx, r.dec, spec, stream.ContainerData, inRes, span)
	if err != nil {
		return res, err
	}
	cp.Status = task.StatusHOLD
	cp.Errors = inRes.Errors.All()
	if _, err := task.SaveCheckpoint(r.work, cp); err != nil {
		return res, err
	}
	res.Status = task.StatusHOLD
	res.HoldOffset = offset
	res.Threshold = boolLabel(hold && offset == corruptOffset)
	res.OK = hold && offset == corruptOffset
	res.Details = append(res.Details, fmt.Sprintf("status=HOLD error_offset=%d", offset))
	return res, nil
}

func boolLabel(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}
