package engine

import (
	"encoding/json"
	"fmt"
	"time"

	"mediasync/internal/checkpoint"
	"mediasync/internal/model"
	"mediasync/internal/output"
	"mediasync/internal/rtp"
)

// checkpointPayload is the resumable state written into each checkpoint: the
// accepted event log prefix and its repair records. Output is a pure function
// of this payload, which is what makes resume reproducible.
type checkpointPayload struct {
	Accepted []accepted    `json:"accepted"`
	Repairs  []adminRepair `json:"repairs"`
	Late     []LateEvent   `json:"late"`
	Packets  int64         `json:"packets"`
	Dups     int64         `json:"dups"`
	LateRec  int64         `json:"late_rec"`
	// Dedup, Ts and Bases preserve the wrap unwrappers and the shared time-base
	// anchors exactly, so the resumed tail is interpreted with the same cycle
	// positions as the uninterrupted run.
	Dedup map[string]rtp.DeduperSnapshot   `json:"dedup"`
	Ts    map[string]rtp.TimestampSnapshot `json:"ts"`
	Bases map[string]model.Nanos           `json:"bases"`
}

// snapshotFields returns the current unwrapper states keyed by track id.
func (e *Engine) snapshotFields() (map[string]rtp.DeduperSnapshot, map[string]rtp.TimestampSnapshot) {
	dedup := make(map[string]rtp.DeduperSnapshot, len(e.dedupers))
	ts := make(map[string]rtp.TimestampSnapshot, len(e.tsExt))
	for id, d := range e.dedupers {
		dedup[itoa(id)] = d.Snapshot()
	}
	for id, t := range e.tsExt {
		ts[itoa(id)] = t.Snapshot()
	}
	return dedup, ts
}

// writeCheckpoint snapshots the log prefix up to and including the event at
// inputByte and appends a digest-protected record.
func (e *Engine) writeCheckpoint(inputByte int64) *model.Error {
	dedup, ts := e.snapshotFields()
	bases := make(map[string]model.Nanos, len(e.bases))
	for id, b := range e.bases {
		bases[itoa(id)] = b
	}
	payload := checkpointPayload{
		Accepted: e.accepted, Repairs: e.repairs, Late: e.late,
		Packets: e.packets, Dups: e.dups, LateRec: e.lateRec,
		Dedup: dedup, Ts: ts, Bases: bases,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return model.NewError(model.CodeCheckpointCorrupt, model.Loc{File: e.store.Dir()}, "encode checkpoint payload").Wrap(err)
	}
	der := deriveRecords(e.accepted, e.repairs, e.tracks, e.order, e.opts)
	rec := checkpoint.Record{
		Index:         e.processed,
		EventSeq:      e.processed,
		InputByte:     inputByte,
		Watermark:     int64(e.wm.Value()),
		DriftPPM:      der.driftPPM,
		DriftSlope:    1 + der.driftPPM/1e6,
		OutputDigest:  output.Digest(der.records),
		Lost:          sumMap(der.lost),
		Recovered:     sumMap(der.recovered),
		LateRecompute: e.lateRec,
		Payload:       encodePayload(body),
	}
	written, werr := e.store.Append(rec)
	if werr != nil {
		var me *model.Error
		if model.As(werr, &me) {
			return me
		}
		return model.NewError(model.CodeCheckpointCorrupt, model.Loc{File: e.store.Dir()}, "write checkpoint").Wrap(werr)
	}
	e.checkpoints++
	e.lastRecord = &written
	return nil
}

func sumMap(m map[uint32]int64) int64 {
	var n int64
	for _, v := range m {
		n += v
	}
	return n
}

// ResumeResult reports a resume attempt.
type ResumeResult struct {
	ResumedFrom int64
	ResumeMS    float64
	Fallback    string
	Records     int
}

// Resume restores the newest checkpoint whose payload verifies, re-derives the
// delivered digest for that prefix, and compares it with the recorded output
// digest. A mismatch is a corrupt checkpoint: it is skipped, and the reason
// names both the checkpoint index and the observed deviation.
func (e *Engine) Resume() (ResumeResult, error) {
	start := time.Now()
	var res ResumeResult
	loaded, err := e.store.Load()
	if err != nil {
		return res, err
	}
	res.Fallback = loaded.FallbackReason
	for _, rec := range loaded.Candidates() {
		var payload checkpointPayload
		body, derr := decodePayload(rec.Payload)
		if derr != nil {
			res.Fallback = fmt.Sprintf("checkpoint %d payload: %v", rec.Index, derr)
			continue
		}
		if derr := json.Unmarshal(body, &payload); derr != nil {
			res.Fallback = fmt.Sprintf("checkpoint %d payload: %v", rec.Index, derr)
			continue
		}
		der := deriveRecords(payload.Accepted, payload.Repairs, e.tracks, e.order, e.opts)
		got := output.Digest(der.records)
		if got != rec.OutputDigest {
			res.Fallback = fmt.Sprintf("checkpoint %d output digest mismatch (recorded=%s recomputed=%s)", rec.Index, rec.OutputDigest, got)
			continue
		}
		e.accepted = append([]accepted(nil), payload.Accepted...)
		e.repairs = append([]adminRepair(nil), payload.Repairs...)
		e.late = append([]LateEvent(nil), payload.Late...)
		e.packets = payload.Packets
		e.dups = payload.Dups
		e.lateRec = payload.LateRec
		e.wm.Restore(model.Nanos(rec.Watermark), wmMax(payload), len(payload.Accepted) > 0)
		e.processed = rec.Index
		e.resumedFrom = rec.Index
		if err := e.restoreUnwrappers(payload); err != nil {
			res.Fallback = fmt.Sprintf("checkpoint %d unwrapper state: %v", rec.Index, err)
			continue
		}
		res.ResumedFrom = rec.Index
		res.Records = len(der.records)
		res.ResumeMS = float64(time.Since(start).Microseconds()) / 1000
		e.resumeMS = res.ResumeMS
		e.fallbackReason = res.Fallback
		return res, nil
	}
	return res, model.NewError(model.CodeCheckpointCorrupt, model.Loc{File: e.store.Dir()}, "no checkpoint payload verified")
}

// restoreUnwrappers installs the saved wrap positions. A payload without
// them rebuilds by snapping each track to its last extended sequence so old
// checkpoints remain loadable.
func (e *Engine) restoreUnwrappers(p checkpointPayload) error {
	if len(p.Dedup) > 0 {
		for k, snap := range p.Dedup {
			id, ok := atou(k)
			if !ok || e.dedupers[id] == nil {
				return fmt.Errorf("unknown track %q", k)
			}
			e.dedupers[id].Restore(snap)
		}
	} else {
		for _, a := range p.Accepted {
			if d := e.dedupers[a.TrackID]; d != nil {
				d.SnapTo(a.Ext)
			}
		}
	}
	for k, snap := range p.Ts {
		id, ok := atou(k)
		if !ok || e.tsExt[id] == nil {
			return fmt.Errorf("unknown track %q", k)
		}
		e.tsExt[id].Restore(snap)
	}
	e.bases = map[uint32]model.Nanos{}
	for k, b := range p.Bases {
		id, ok := atou(k)
		if !ok {
			return fmt.Errorf("unknown track %q", k)
		}
		e.bases[id] = b
	}
	return nil
}

// wmMax is the largest corrected event time in the payload.
func wmMax(p checkpointPayload) model.Nanos {
	var m model.Nanos
	for _, a := range p.Accepted {
		if a.Raw > m {
			m = a.Raw
		}
	}
	return m
}
