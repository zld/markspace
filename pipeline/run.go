package pipeline

import (
	"fmt"
	"math"
	"sort"

	"mediad/index"
	"mediad/media"
	"mediad/timeline"
)

// RunOptions controls a batch execution.
type RunOptions struct {
	// StopAfterChunks, when positive, commits that many new chunks and returns a
	// partial result without publishing. It exists to exercise resume.
	StopAfterChunks int
	// LatencySamplesMs records per-stage latency figures for the p95 report.
	LatencySamplesMs []float64
}

// RunResult is what a batch run produced.
type RunResult struct {
	Manifest *Manifest
	// Partial is true when StopAfterChunks ended the run early.
	Partial bool
	// Output is the published JSON, nil when partial or refused.
	Output []byte
	// Issues are all recorded anomalies.
	Issues timeline.Issues
	// ResumedFromChunk is the last contiguous committed chunk found at start,
	// or -1 for a fresh run.
	ResumedFromChunk int
	// CommittedChunks is the number of chunks durable after the run.
	CommittedChunks int
}

// Run executes the batch: align renditions, partition segments into fixed
// chunks, skip already-committed chunks, commit each new chunk after
// re-verifying inputs, then build and publish the manifest.
//
// A resumed run and a continuous run produce the same manifest digest because
// the manifest is derived only from (inputs, config, parsed results) and every
// chunk's summary is derived from the same segment bytes regenerated on each
// run. The checkpoint decides which chunks are *recomputed*, never what they
// contain.
func (j *Job) Run(inputs []Input, opts RunOptions) (*RunResult, error) {
	res := &RunResult{ResumedFromChunk: -1}

	scanIssues, scanErr := j.ScanAll(inputs)
	res.Issues.Merge(scanIssues)
	if scanErr != nil {
		return res, scanErr
	}

	renditions, tracksByName, err := j.renditions()
	if err != nil {
		return res, err
	}
	alignRes, alignErr := media.AlignSegments(renditions, media.AlignOptions{
		TargetSegmentUs:    j.Config.SegmentTargetUs,
		MaxBoundaryErrorUs: j.Config.MaxBoundaryErrorUs,
	})
	res.Issues.Merge(alignRes.Issues)

	segments, segIssues := buildSegments(alignRes, tracksByName)
	res.Issues.Merge(segIssues)
	if alignErr != nil {
		res.Issues.AddError(timeline.CodeNoBoundary, alignErr)
	}

	// Any fatal issue anywhere means the inputs are damaged; still commit what
	// was computed so a later fixed rerun can resume, but refuse to publish.
	fatal := res.Issues.HasErrors()

	headerInputs, err := j.verifyInputs()
	if err != nil {
		return res, err
	}
	header := index.CheckpointHeader{Inputs: headerInputs, ChunkSize: j.Config.ChunkSize, ConfigSum: j.Config.Sum()}

	cp, err := j.Checkpoint.Load()
	if err != nil {
		return res, err
	}
	res.ResumedFromChunk = cp.MaxCommitted()

	chunks := chunkSegments(segments, j.Config.ChunkSize)
	committed := cp.Committed()
	newlyCommitted := 0
	for _, chunk := range chunks {
		if _, ok := committed[chunk.seq]; ok {
			continue
		}
		if opts.StopAfterChunks > 0 && newlyCommitted >= opts.StopAfterChunks {
			res.Partial = true
			break
		}
		fresh, verr := j.verifyInputs()
		if verr != nil {
			return res, verr
		}
		if !fingerprintsEqual(fresh, header.Inputs) {
			return res, &timeline.LocatedError{Issue: timeline.Issue{
				Severity: timeline.SeverityError, Code: timeline.CodeFingerprint,
				Message: "input bytes changed during the run; refusing to commit a chunk from stale data"}}
		}
		if _, err := j.Checkpoint.Commit(header, index.ChunkResult{
			Seq:      chunk.seq,
			Summary:  chunkSummary(chunk.segments),
			Segments: len(chunk.segments),
		}); err != nil {
			return res, err
		}
		newlyCommitted++
	}

	finalCP, err := j.Checkpoint.Load()
	if err != nil {
		return res, err
	}
	res.CommittedChunks = len(finalCP.Chunks)
	complete := !res.Partial && finalCP.MaxCommitted() == len(chunks)-1

	if res.Partial {
		return res, nil
	}

	manifest := j.buildManifest(inputs, alignRes, segments, res.Issues, opts)
	manifest.Complete = complete
	res.Manifest = manifest
	if !complete {
		return res, &timeline.LocatedError{Issue: timeline.Issue{
			Severity: timeline.SeverityError, Code: timeline.CodeCheckpoint,
			Message: fmt.Sprintf("only %d of %d chunks are committed", res.CommittedChunks, len(chunks))}}
	}
	if fatal {
		// Name the source position of the first fatal issue so the refusal is
		// actionable rather than a generic "errors recorded" message.
		first := firstFatal(res.Issues)
		msg := "refusing to publish: fatal input or alignment issues"
		if first != nil {
			msg = "refusing to publish: " + first.String()
		}
		return res, &timeline.LocatedError{Issue: timeline.Issue{
			Severity: timeline.SeverityError, Code: timeline.CodeRefused, Message: msg}}
	}
	out, err := manifest.Publish()
	if err != nil {
		return res, err
	}
	res.Output = out
	return res, nil
}

type segmentChunk struct {
	seq      int
	segments []SegmentInfo
}

// chunkSegments partitions segments into fixed-size chunks.
func chunkSegments(segments []SegmentInfo, size int) []segmentChunk {
	if size <= 0 {
		size = 1
	}
	var out []segmentChunk
	for start, seq := 0, 0; start < len(segments); start, seq = start+size, seq+1 {
		end := start + size
		if end > len(segments) {
			end = len(segments)
		}
		out = append(out, segmentChunk{seq: seq, segments: segments[start:end]})
	}
	return out
}

// chunkSummary derives a chunk's manifest contribution from its segments. It
// depends on segment bytes only, so it is identical whether this run or a prior
// one produced the chunk.
func chunkSummary(segments []SegmentInfo) string {
	return SegmentDigest(SegmentInfo{Index: segments[0].Index, Renditions: mergeRenditions(segments)})
}

// mergeRenditions folds a chunk's per-segment ranges into one map: the first
// range's offset, the last range's end, and the max boundary error per
// rendition. That is enough to detect a changed cut or byte span.
func mergeRenditions(segments []SegmentInfo) map[string]SegmentByteRange {
	out := map[string]SegmentByteRange{}
	for _, s := range segments {
		for name, r := range s.Renditions {
			if prev, ok := out[name]; ok {
				prev.LastSample = r.LastSample
				prev.Length = r.Offset + r.Length - prev.Offset
				if r.BoundaryErrorUs > prev.BoundaryErrorUs {
					prev.BoundaryErrorUs = r.BoundaryErrorUs
				}
				out[name] = prev
			} else {
				out[name] = r
			}
		}
	}
	return out
}

// fingerprintsEqual reports whether two input maps agree exactly.
func fingerprintsEqual(a, b map[string]index.Fingerprint) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if other, ok := b[k]; !ok || !v.Equal(other) {
			return false
		}
	}
	return true
}

// computeJobID is a stable short id over inputs and config.
func computeJobID(inputs map[string]index.Fingerprint, cfg Config) string {
	parts := []string{cfg.Sum()}
	for _, p := range sortedKeys(inputs) {
		parts = append(parts, p+":"+inputs[p].Content)
	}
	full := index.ConfigSum(parts...)
	return full[:16]
}

// Percentile returns the p-th percentile (0..100) of samples using nearest-rank.
func Percentile(samples []float64, p float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]float64(nil), samples...)
	sort.Float64s(sorted)
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	rank := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

// buildSegments converts alignment output into manifest segments with byte
// ranges resolved against each rendition's source file.
func buildSegments(align media.AlignResult, tracksByName map[string]media.Track) ([]SegmentInfo, timeline.Issues) {
	var out []SegmentInfo
	var issues timeline.Issues
	for _, seg := range align.Segments {
		si := SegmentInfo{Index: seg.Index, StartUs: seg.Start, EndUs: seg.End, Renditions: map[string]SegmentByteRange{}}
		for name, rs := range seg.ByRendition {
			t, ok := tracksByName[name]
			if !ok {
				issues.Add(timeline.Issue{Severity: timeline.SeverityError, Code: timeline.CodeMissingTrack,
					Message: fmt.Sprintf("segment %d references unknown rendition %q", seg.Index, name)})
				continue
			}
			start, end, err := media.SamplePath(t, rs.FirstSample, rs.LastSample)
			if err != nil {
				issues.Add(timeline.Issue{Severity: timeline.SeverityError, Code: timeline.CodeNoBoundary,
					File: t.SourcePath, TrackID: t.ID, Message: err.Error()})
				continue
			}
			si.Renditions[name] = SegmentByteRange{
				FirstSample:     rs.FirstSample,
				LastSample:      rs.LastSample,
				Offset:          start,
				Length:          end - start,
				BoundaryErrorUs: rs.BoundaryErrorUs,
			}
		}
		si.Digest = SegmentDigest(si)
		out = append(out, si)
	}
	return out, issues
}

// firstFatal returns the first fatal issue in sorted order, or nil.
func firstFatal(is timeline.Issues) *timeline.Issue {
	for _, i := range is.Sorted() {
		if i.Severity != timeline.SeverityWarning {
			issue := i
			return &issue
		}
	}
	return nil
}
