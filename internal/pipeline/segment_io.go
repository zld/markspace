package pipeline

import (
	"os"
	"path/filepath"

	"mediarun/internal/fault"
	"mediarun/internal/frames"
	"mediarun/internal/index"
	mp4mux "mediarun/internal/mp4"
	"mediarun/internal/provenance"
	"mediarun/internal/syncer"
)

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
		Incomplete:   rep.Incomplete || plan.Incomplete,
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
