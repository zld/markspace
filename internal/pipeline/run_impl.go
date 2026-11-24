package pipeline

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash"
	"os"
	"path/filepath"

	"mediarun/internal/fault"
	"mediarun/internal/index"
	mp4mux "mediarun/internal/mp4"
	"mediarun/internal/provenance"
	"mediarun/internal/syncer"
)

// syncerConfig is an alias so syncConfig can return the syncer configuration
// without importing syncer in run.go as well.
type syncerConfig = syncer.Config

func newDigest() hash.Hash { return sha256.New() }

// executeSegments runs sync, mux and index for every planned segment, reusing
// previously written outputs when their stage hash matches the checkpoint.
func (r *Runner) executeSegments() error {
	if len(r.plans) == 0 {
		return nil
	}
	segHash := stageHash("mux", r.cfg.Hash(), r.hashes.frames, r.hashes.audio, recordsDigest(r.records))
	prev, hadPrev := r.cp.Hashes["mux"]

	for i := range r.plans {
		plan := &r.plans[i]
		rep, err := r.alignSegment(plan, r.timeline)
		if err != nil {
			return err
		}
		if err := r.cp.Mark(r.dir, "sync", stageHash("sync", r.cfg.Hash(), r.hashes.frames, alignmentDigest(rep))); err != nil {
			return err
		}

		path := segmentPath(r.dir, plan.Index)
		// Reuse an existing segment only when the previous run produced it
		// under an identical mux hash and both files still verify.
		reuse := hadPrev && prev == segHash && fileExists(path) && fileExists(indexPath(r.dir, plan.Index))
		var seg provenance.Segment
		if reuse {
			if err := r.loadOrRebuildSegment(plan, rep, &seg, true); err != nil {
				return err
			}
			r.runtime.skippedStages = append(r.runtime.skippedStages, fmt.Sprintf("segment:%d", plan.Index))
		} else {
			if err := r.writeSegment(plan, rep, &seg); err != nil {
				return err
			}
		}
		r.segments = append(r.segments, seg)
		r.segHashes = append(r.segHashes, seg.SHA256)
		r.metrics.Segments++
		r.metrics.RecoveredAudio += countRecovered(plan.Audio)
		r.metrics.KeyframeTotal += seg.Keyframes
		r.metrics.KeyframeHits += seg.KeyframeHits
		r.metrics.RoundtripTotal += seg.Samples
		r.metrics.RoundtripHits += seg.RoundtripHit
		if seg.Incomplete {
			r.metrics.IncompleteSegments++
		}
		if !seg.Synced {
			r.metrics.UnsyncedSegments++
		}
	}
	if !hadPrev || prev != segHash {
		if err := r.cp.Mark(r.dir, "mux", segHash); err != nil {
			return err
		}
	}
	return nil
}

// writeSegment performs the mux and index stages for one segment. The MP4 is
// written to a temporary file and renamed only after the index and a payload
// roundtrip both verify.
func (r *Runner) writeSegment(plan *SegmentPlan, rep *syncer.Report, seg *provenance.Segment) error {
	payloads, err := r.loadPayloads(plan.Frames)
	if err != nil {
		return err
	}
	// Attach the verified payloads to the records for the duration of the
	// write, then release them: the planning pass keeps only metadata, so at
	// most one segment's payloads are ever resident.
	for i, rec := range plan.Frames {
		rec.Payload = payloads[i]
	}
	defer func() {
		for _, rec := range plan.Frames {
			rec.Payload = nil
		}
	}()
	samples := make([]mp4mux.Sample, len(rep.Video))
	for i, vf := range rep.Video {
		samples[i] = mp4mux.Sample{Record: vf.Record}
	}
	segPath := segmentPath(r.dir, plan.Index)
	if err := os.MkdirAll(filepath.Dir(segPath), 0o755); err != nil {
		return fault.Wrap(fault.StageMux, fault.KindIO, err, "create %s", filepath.Dir(segPath))
	}
	tmp, err := os.CreateTemp(filepath.Dir(segPath), ".segment-*.tmp")
	if err != nil {
		return fault.Wrap(fault.StageMux, fault.KindIO, err, "temp segment")
	}
	tmpName := tmp.Name()
	cleanup := func() { os.Remove(tmpName) }

	muxCfg := mp4mux.Config{
		Timescale:      r.cfg.Timescale,
		AudioTimescale: r.cfg.AudioClockRate,
		Width:          r.cfg.Width,
		Height:         r.cfg.Height,
		MaxSamples:     r.cfg.MaxRecords,
	}
	res, err := mp4mux.Mux(tmp, rep, r.timeline, muxCfg)
	if err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return fault.Wrap(fault.StageMux, fault.KindIO, err, "sync segment")
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fault.Wrap(fault.StageMux, fault.KindIO, err, "close segment")
	}

	idx := &index.Index{
		Segment:     plan.Index,
		Entries:     res.Keyframes,
		VideoTrack:  res.VideoTrack,
		SampleCount: res.Samples,
		Samples:     sampleRefs(res, samples),
	}
	vr, err := index.Verify(tmpName, idx)
	if err != nil {
		cleanup()
		return err
	}
	if !vr.Verified() {
		cleanup()
		return fault.New(fault.StageIndex, fault.KindIndex,
			"segment %d: %d/%d keyframes resolved", plan.Index, vr.Hits, vr.Checked)
	}
	// Every video sample payload must still hash to its ingest digest: the mux
	// preserves bytes, so a mismatch means the chunk layout is wrong.
	rtHits, rtErr := r.roundtripCheck(tmpName, res, samples)
	if rtErr != nil {
		cleanup()
		return rtErr
	}
	if rtHits != len(samples) {
		cleanup()
		return fault.New(fault.StageMux, fault.KindIndex,
			"segment %d: %d/%d samples roundtrip", plan.Index, rtHits, len(samples))
	}
	if err := writeIndexFile(r.dir, plan.Index, idx); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, segPath); err != nil {
		cleanup()
		return fault.Wrap(fault.StageMux, fault.KindIO, err, "rename segment")
	}
	sum, n, err := provenance.HashFile(segPath)
	if err != nil {
		return fault.Wrap(fault.StagePipeline, fault.KindIO, err, "hash segment")
	}
	r.cp.RecordSegment(plan.Index, sum)
	*seg = provenance.Segment{
		Index:        plan.Index,
		Path:         filepath.ToSlash(filepath.Join(SegmentsDir, segmentName(plan.Index))),
		Bytes:        n,
		SHA256:       sum,
		Samples:      res.Samples,
		Keyframes:    len(idx.Entries),
		KeyframeHits: vr.Hits,
		RoundtripHit: rtHits,
		Synced:       rep.Synced,
		Incomplete:   rep.Incomplete || plan.Incomplete,
		AudioFrames:  res.AudioFrames,
		AudioBytes:   res.AudioBytes,
		Faults:       segmentFaults(rep),
	}
	if len(rep.Video) > 0 {
		seg.StartPTS = uint64(rep.Video[0].LocalTick)
		seg.EndPTS = uint64(rep.VideoEnd)
	}
	if err := r.cp.Mark(r.dir, "index", stageHash("index", r.cfg.Hash(), sum)); err != nil {
		return err
	}
	return nil
}

// samplePayload mirrors mp4mux.Sample's payload override.
func samplePayload(s mp4mux.Sample) []byte {
	if s.Payload != nil {
		return s.Payload
	}
	return s.Record.Payload
}

func alignmentDigest(rep *syncer.Report) string {
	h := newDigest()
	for _, v := range rep.Video {
		fmt.Fprintf(h, "v|%d|%d\n", v.Record.Offset, v.LocalTick)
	}
	for _, a := range rep.Audio {
		fmt.Fprintf(h, "a|%d|%d|%d\n", a.Unit.Seq, a.LocalTick, a.DurationTicks)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func countRecovered(units []syncer.Placed) int {
	n := 0
	for _, u := range units {
		if u.Unit.Silence {
			n++
		}
	}
	return n
}

func segmentFaults(rep *syncer.Report) []provenance.FaultRecord {
	out := make([]provenance.FaultRecord, 0, len(rep.Faults))
	for _, e := range rep.Faults {
		out = append(out, provenance.FromFault(e))
	}
	provenance.SortFaults(out)
	return out
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func indexPath(dir string, seg int) string {
	return filepath.Join(dir, IndexDir, segmentName(seg)+".json")
}

func writeIndexFile(dir string, seg int, idx *index.Index) error {
	idx.Sort()
	data, err := provenance.CanonicalJSON(idx)
	if err != nil {
		return fault.Wrap(fault.StageIndex, fault.KindIO, err, "marshal index")
	}
	return provenance.WriteFileAtomic(indexPath(dir, seg), data)
}

func readIndexFile(dir string, seg int) (*index.Index, error) {
	data, err := os.ReadFile(indexPath(dir, seg))
	if err != nil {
		return nil, fault.Wrap(fault.StageIndex, fault.KindIO, err, "read index")
	}
	var idx index.Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fault.Wrap(fault.StageIndex, fault.KindIndex, err, "parse index")
	}
	return &idx, nil
}
