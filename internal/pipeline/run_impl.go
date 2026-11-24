package pipeline

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash"
	"os"
	"path/filepath"

	"mediarun/internal/fault"
	"mediarun/internal/frames"
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
		Incomplete:   rep.Incomplete || r.runtime.audioIncomplete,
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

// loadOrRebuildSegment reuses a verified segment from a previous run.
func (r *Runner) loadOrRebuildSegment(plan *SegmentPlan, rep *syncer.Report, seg *provenance.Segment, reuse bool) error {
	if !reuse {
		return r.writeSegment(plan, rep, seg)
	}
	segPath := segmentPath(r.dir, plan.Index)
	sum, n, err := provenance.HashFile(segPath)
	if err != nil {
		return fault.Wrap(fault.StagePipeline, fault.KindIO, err, "hash cached segment")
	}
	want, ok := r.cp.SegmentHash(plan.Index)
	if !ok || want != sum {
		// The file on disk is not the one this checkpoint produced (foreign,
		// hand-edited, or from a run that did not record a digest). Rebuild it
		// rather than trusting it.
		return r.writeSegment(plan, rep, seg)
	}
	idxf, err := readIndexFile(r.dir, plan.Index)
	if err != nil {
		return r.writeSegment(plan, rep, seg)
	}
	vr, err := index.Verify(segPath, idxf)
	if err != nil || !vr.Verified() {
		return r.writeSegment(plan, rep, seg)
	}
	sr, err := index.VerifySamples(segPath, idxf)
	if err != nil || !sr.Verified() {
		return r.writeSegment(plan, rep, seg)
	}
	frames, audioBytes := audioStats(rep)
	*seg = provenance.Segment{
		Index:        plan.Index,
		Path:         filepath.ToSlash(filepath.Join(SegmentsDir, segmentName(plan.Index))),
		Bytes:        n,
		SHA256:       sum,
		Samples:      idxf.SampleCount,
		Keyframes:    idxf.Count(),
		KeyframeHits: vr.Hits,
		RoundtripHit: sr.Hits,
		Synced:       rep.Synced,
		Incomplete:   rep.Incomplete || r.runtime.audioIncomplete,
		AudioFrames:  frames,
		AudioBytes:   audioBytes,
		Faults:       segmentFaults(rep),
	}
	if len(rep.Video) > 0 {
		seg.StartPTS = uint64(rep.Video[0].LocalTick)
		seg.EndPTS = uint64(rep.VideoEnd)
	}
	return nil
}

// roundtripCheck re-reads each sample payload and compares digests.
func (r *Runner) roundtripCheck(path string, res *mp4mux.Result, samples []mp4mux.Sample) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fault.Wrap(fault.StageMux, fault.KindIO, err, "open segment")
	}
	defer f.Close()
	tbl, err := index.ReadTable(path)
	if err != nil {
		return 0, err
	}
	offs, err := index.SampleOffsets(tbl)
	if err != nil {
		return 0, err
	}
	hits := 0
	for i := range samples {
		if i >= len(offs) {
			break
		}
		want := index.PayloadHash(samplePayload(samples[i]))
		size := int(tbl.SampleSizes[i])
		buf := make([]byte, size)
		if _, err := f.ReadAt(buf, offs[i]); err != nil {
			return hits, fault.Wrap(fault.StageMux, fault.KindIO, err, "read sample %d", i)
		}
		if index.PayloadHash(buf) == want {
			hits++
		}
	}
	return hits, nil
}

// loadPayloads re-reads verified payloads for the frames of one segment.
func (r *Runner) loadPayloads(recs []*frames.Record) ([][]byte, error) {
	f, err := os.Open(r.inputs.FramesPath)
	if err != nil {
		return nil, fault.Wrap(fault.StageParse, fault.KindIO, err, "reopen frames")
	}
	defer f.Close()
	out := make([][]byte, len(recs))
	for i, rec := range recs {
		data, err := frames.ReadPayload(f, rec)
		if err != nil {
			return nil, err
		}
		out[i] = data
	}
	return out, nil
}

// audioStats derives the manifest audio counters from the alignment report so
// a resumed run reports the same numbers as a fresh one without re-reading the
// segment.
func audioStats(rep *syncer.Report) (frames int, bytes int) {
	for _, slot := range rep.Audio {
		if slot.Unit.Packet != nil {
			bytes += len(slot.Unit.Packet.Payload)
		}
	}
	return len(rep.Audio), bytes
}

// sampleRefs pairs every exported sample with its ingest coordinates.
func sampleRefs(res *mp4mux.Result, samples []mp4mux.Sample) []index.SampleRef {
	refs := make([]index.SampleRef, len(samples))
	for i, s := range samples {
		ref := index.SampleRef{
			Sample:      i + 1,
			SourceOff:   s.Record.Offset,
			SourceHash:  s.Record.Meta.Checksum,
			PayloadHash: index.PayloadHash(samplePayload(s)),
			Size:        len(samplePayload(s)),
		}
		if i < len(res.SampleOffsets) {
			ref.Size = int(res.SizeList[i])
		}
		refs[i] = ref
	}
	return refs
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
