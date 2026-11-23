package pipeline

// commitSegments buckets frames into SegmentCount journal stages and records
// each frame's owning segment id on its timeline input.
func (r *Runner) commitSegments(frames []collectedFrame) error {
	if len(frames) == 0 {
		return nil
	}
	bucketSize := (len(frames) + SegmentCount - 1) / SegmentCount
	for i, cf := range frames {
		segIdx := i / bucketSize
		if segIdx >= SegmentCount {
			segIdx = SegmentCount - 1
		}
		if cf.segByFrame != nil {
			cf.segByFrame[cf.fr] = segmentName(segIdx)
		}
	}
	// Commit one artifact set per segment so the WAL has SegmentCount stages.
	for segIdx := 0; segIdx < SegmentCount; segIdx++ {
		artifacts := map[string][]byte{}
		lo := segIdx * bucketSize
		hi := lo + bucketSize
		if hi > len(frames) {
			hi = len(frames)
		}
		keys := make([]string, 0, hi-lo)
		for k := lo; k < hi; k++ {
			cf := frames[k]
			key := "frame-" + pad4(k) + ".bin"
			artifacts[key] = cf.fr.Bytes
			keys = append(keys, key)
		}
		if len(artifacts) == 0 {
			continue
		}
		stage := segmentName(segIdx)
		if err := r.coord.CommitStage(stage, artifacts); err != nil {
			return err
		}
		// Only after the COMMIT record is durable do these artifacts count.
		for i, k := range keys {
			cf := frames[lo+i]
			r.committedFrames[frameKey{track: cf.ssrc, rtpTS: cf.fr.Timestamp}] =
				committedArtifact{stage: stage, key: k, sha: sha256Hex(cf.fr.Bytes)}
		}
	}
	return nil
}
