package mediacore

import "fmt"

// DemoReport is the machine-readable output of the demo command.
type DemoReport struct {
	Seed            int64         `json:"seed"`
	Repeat          int           `json:"repeat"`
	RoundtripRate   float64       `json:"roundtrip_rate"`
	KeyframeHitRate float64       `json:"keyframe_hit_rate"`
	SegmentSHA      string        `json:"segment_sha256"`
	PerSegmentSHA   []string      `json:"segment_sha256_list"`
	DuplicateCount  uint64        `json:"duplicate_count"`
	MissingCount    uint64        `json:"missing_count"`
	RecoveredLate   uint64        `json:"recovered_late"`
	DiscardedLate   uint64        `json:"discarded_late"`
	IndexRebuilt    bool          `json:"index_rebuilt"`
	TrackSummaries  []string      `json:"track_summaries"`
	TrackHashes     []string      `json:"track_hashes"`
	Faults          []FaultReport `json:"faults"`
	GapReports      []string      `json:"gap_reports"`
	InputDigest     string        `json:"input_digest"`
	MutatedDigest   string        `json:"mutated_digest"`
	Checks          []CheckReport `json:"checks"`
	ExitCode        int           `json:"exit_code"`
	BandwidthSteps  int           `json:"bandwidth_steps"`
	PacedPackets    int           `json:"paced_packets"`
	RTPPayloadSum   string        `json:"rtp_payload_sha256"`
	TimelineDrift   int64         `json:"timeline_drift_ticks"`
	ResumePoint     int           `json:"resume_segment"`
	Deterministic   bool          `json:"deterministic"`
}

// FaultReport is the classified behaviour of one corrupted sample.
type FaultReport struct {
	Name string    `json:"name"`
	Kind ErrorKind `json:"kind"`
	Err  string    `json:"error,omitempty"`
}

// CheckReport is an individual acceptance assertion.
type CheckReport struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}

// Run executes the full demo: fixture generation, lossless remux with
// verification, index rebuild detection, fault classification, RTP recovery
// under loss/reorder/duplication/pacing, repeat determinism and resume.
func (d *DemoRunner) Run() (*DemoReport, error) {
	dir := d.opts.WorkDir
	fixtures, err := GenerateFixtures(dir, d.opts.Seed)
	if err != nil {
		return nil, err
	}
	pipeline := DefaultPipeline()
	pipeline.Scan.SegmentDuration = d.segmentDuration()
	report := &DemoReport{Seed: d.opts.Seed}

	cacheDir := dir + "/cache"
	first, err := pipeline.RunIndexPipeline(fixtures.MP4, cacheDir, dir+"/normal.out.mp4")
	if err != nil {
		return nil, err
	}
	first.DigestLabel = short(first.Input.Content)
	report.RoundtripRate = first.RoundtripRate
	report.KeyframeHitRate = first.KeyframeHitRate
	report.SegmentSHA = first.Hashes.Combined
	for _, s := range first.Hashes.Segments {
		report.PerSegmentSHA = append(report.PerSegmentSHA, s.SHA256)
	}
	report.IndexRebuilt = first.IndexRebuilt
	report.InputDigest = first.Input.Content
	report.TrackSummaries = trackSummaries(first.Index)
	report.TrackHashes = trackHashes(first.Index)
	report.ExitCode = 0
	d.p("== index pipeline ==")
	d.p("input    %s (%d bytes) digest=%s", fixtures.MP4, first.Input.Size, short(first.Input.Content))
	d.p("roundtrip_rate=%.3f keyframe_hit_rate=%.3f index_rebuilt=%t (%s)",
		report.RoundtripRate, report.KeyframeHitRate, report.IndexRebuilt, first.IndexReason)
	for _, s := range report.TrackSummaries {
		d.p("  %s", s)
	}
	d.p("segment_sha256=%s", report.SegmentSHA)

	// Second run of the same input must reuse the cache and hash identically.
	second, err := pipeline.RunIndexPipeline(fixtures.MP4, cacheDir, dir+"/normal2.out.mp4")
	if err != nil {
		return nil, err
	}
	report.Deterministic = second.Hashes.Combined == first.Hashes.Combined
	report.Checks = append(report.Checks, CheckReport{"identical_segment_hashes", report.Deterministic, report.SegmentSHA})
	resumeOK, resumeDetail := verifyResume(first.Hashes)
	report.Checks = append(report.Checks, CheckReport{"resume_by_digest_and_boundary", resumeOK, resumeDetail})
	report.ResumePoint = resumeSegment(first.Hashes)
	d.p("second_run segment_sha256=%s deterministic=%t rebuilt=%t", second.Hashes.Combined, report.Deterministic, second.IndexRebuilt)

	// Mutating one media byte must invalidate the cache and change hashes.
	// Reuse the SAME cache directory as the valid run: the cached index is
	// bound to the original digest, so mutating one media byte must be detected
	// as "input digest changed" and force a rebuild rather than a cache hit.
	mut, err := pipeline.RunIndexPipeline(fixtures.Mutated, cacheDir, dir+"/mutated.out.mp4")
	if err != nil {
		return nil, err
	}
	report.MutatedDigest = mut.Input.Content
	digestChanged := mut.Input.Content != first.Input.Content
	rebuildAfterMutate := mut.IndexRebuilt && digestChanged
	report.Checks = append(report.Checks,
		CheckReport{"digest_changed_after_mutation", digestChanged, short(mut.Input.Content)},
		CheckReport{"index_rebuilt_after_mutation", rebuildAfterMutate, mut.IndexReason})
	d.p("mutated  digest=%s changed=%t index_rebuilt=%t", short(mut.Input.Content), digestChanged, mut.IndexRebuilt)

	// Fault fixtures: each must fail with a decidable classification.
	faults := []struct{ name, path string }{
		{"truncated_moov", fixtures.Truncated},
		{"sample_offset_out_of_range", fixtures.BadOffset},
	}
	for _, f := range faults {
		_, err := pipeline.RunIndexPipeline(f.path, cacheDir+"/faults", "")
		fr := FaultReport{Name: f.name}
		if err == nil {
			fr.Kind = "internal"
			fr.Err = "corrupt input accepted unexpectedly"
		} else {
			fr.Kind = KindOf(err)
			fr.Err = err.Error()
		}
		report.Faults = append(report.Faults, fr)
		d.p("fault    %s -> kind=%s err=%s", fr.Name, fr.Kind, fr.Err)
	}

	rtpResult, err := d.runRTP(fixtures, report)
	if err != nil {
		return nil, err
	}
	report.RTPPayloadSum = rtpResult
	report.DuplicateCount = 0
	report.MissingCount = 0
	if d.rtpStats != nil {
		report.DuplicateCount = d.rtpStats.Duplicates
		report.MissingCount = d.rtpStats.Missing
		report.RecoveredLate = d.rtpStats.LateFilled
		report.DiscardedLate = d.rtpStats.Discarded
		for _, g := range d.rtpStats.Gaps {
			line := fmt.Sprintf("track=%d stream=%s seq=[%d,%d] count=%d state=%s", g.TrackID, g.Stream, g.FirstSeq, g.LastSeq, g.Count(), g.State)
			report.GapReports = append(report.GapReports, line)
			d.p("gap    %s", line)
		}
	}
	report.Checks = append(report.Checks,
		CheckReport{"roundtrip_rate", report.RoundtripRate == 1, fmt.Sprintf("%.3f", report.RoundtripRate)},
		CheckReport{"keyframe_hit_rate", report.KeyframeHitRate == 1, fmt.Sprintf("%.3f", report.KeyframeHitRate)},
		CheckReport{"duplicates_counted_once", report.DuplicateCount == uint64(d.opts.Duplicate), fmt.Sprintf("%d", report.DuplicateCount)},
		CheckReport{"missing_matches_loss_burst", report.MissingCount == uint64(boundedLoss(d.opts.LossBurst)), fmt.Sprintf("%d", report.MissingCount)},
		CheckReport{"no_fabricated_samples", d.rtpStats != nil && d.rtpStats.Unique+report.MissingCount == uint64(d.rtpPackets), fmt.Sprintf("unique=%d missing=%d packets=%d", uniqueOf(d.rtpStats), report.MissingCount, d.rtpPackets)},
	)
	for _, c := range report.Checks {
		d.p("check    %-32s pass=%t %s", c.Name, c.Pass, c.Detail)
		if !c.Pass {
			report.ExitCode = 1
		}
	}
	return report, nil
}

func (d *DemoRunner) segmentDuration() int64 { return 1000 }

func trackSummaries(idx *Index) []string {
	var out []string
	for _, t := range idx.Tracks {
		out = append(out, fmt.Sprintf("track %d %s %s timescale=%d start=%d end=%d samples=%d duration=%d keyframes=%d hash=%s",
			t.TrackID, t.Kind, t.Codec, t.Timescale, t.StartTicks, t.EndTicks, t.Samples, t.Duration, len(t.Keyframes), short(t.TrackHash)))
	}
	return out
}

func trackHashes(idx *Index) []string {
	var out []string
	for _, t := range idx.Tracks {
		out = append(out, t.TrackHash)
	}
	return out
}
