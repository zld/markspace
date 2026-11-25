package selftest

import (
	"path/filepath"

	"mediacore/internal/fault"
	"mediacore/internal/manifest"
	"mediacore/internal/maptrack"
	"mediacore/internal/pipeline"
	"mediacore/internal/remux"
	"mediacore/internal/scan"
	"mediacore/internal/verify"
)

func runRTPCase(work string) (*CaseReport, error) {
	input := filepath.Join(work, "inputs", "lossy-reordered-duplicate.rtpbin")
	output := filepath.Join(work, "outputs", "lossy-recovered.mp4")
	mf := filepath.Join(work, "manifests", "lossy.json")
	cache := filepath.Join(work, "cache", "lossy.json")
	if err := scan.WriteRTPFixture(input, buildLossyRTPSpec()); err != nil {
		return nil, err
	}
	params := scan.DefaultParams()
	// Warm separately so the audited manifest is identical on cold and repeated
	// command invocations with an unchanged source.
	if _, _, err := scan.LoadOrBuild(input, cache, params); err != nil {
		return nil, err
	}
	idx, rebuilt, err := scan.LoadOrBuild(input, cache, params)
	if err != nil {
		return nil, err
	}
	mappings, err := maptrack.Map(idx)
	if err != nil {
		return nil, err
	}
	rec, samples, err := pipeline.RecoverRTP(idx, params.ReorderWindow)
	if err != nil {
		return nil, err
	}
	if rec.Stats.DuplicatesRemoved != 4 {
		return nil, fault.New(fault.Verification, "expected four duplicate RTP packets")
	}
	if rec.Stats.MissingPackets != 10 || len(rec.Gaps) != 2 {
		return nil, fault.New(fault.Verification, "expected two gaps totaling ten missing packets")
	}
	if rec.Stats.ReorderedPackets < 50 {
		return nil, fault.New(fault.Verification, "expected deterministic heavy reordering")
	}

	if _, err := remux.WriteMP4(idx, mappings, samples, remux.Options{
		OutputPath: output, AllowedRoot: work, Resume: true,
	}); err != nil {
		return nil, err
	}
	verified, err := verify.VerifySamples(output, mappings, samples, 0)
	if err != nil {
		return nil, err
	}

	m := manifest.New("lossy-reordered-duplicate-burst-loss")
	m.Input = digestFile(input, idx.SourceSHA256)
	m.Output = digestFile(output, "")
	m.Index = manifest.IndexRecord{CachePath: cache, SourceSHA256: idx.SourceSHA256, IndexRebuilt: b2i(rebuilt)}
	m.Tracks = manifest.BuildTracks(idx, mappings)
	m.TimeRanges = manifest.BuildTimeRanges(verified.OutputIndex.Samples)
	m.Gaps = rec.Gaps
	m.Metrics = verified.Metrics
	m.Recovery = manifest.RecoveryCounts{
		DuplicatesRemoved: rec.Stats.DuplicatesRemoved,
		ReorderedPackets:  rec.Stats.ReorderedPackets,
		MissingPackets:    rec.Stats.MissingPackets,
		LateRejected:      rec.Stats.LateRejected,
		IndexRebuilt:      b2i(rebuilt),
	}
	m.Assertions = []string{
		"duplicates_removed=4",
		"burst_missing_packets=10",
		"gaps_preserved",
		"no_fabricated_samples",
	}
	if err := manifest.WriteJSON(mf, m); err != nil {
		return nil, err
	}
	hash, _ := manifest.CanonicalSHA256(m)
	return &CaseReport{
		Name: m.Case, OK: true, ManifestPath: mf, ManifestSHA256: hash,
		OutputPath: output, OutputSHA256: m.Output.SHA256,
		Recovery: m.Recovery, Metrics: m.Metrics, Gaps: len(m.Gaps),
		Detail: "four duplicates, two burst gaps",
	}, nil
}
