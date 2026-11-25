package selftest

import (
	"os"

	"mediacore/internal/clock"
	"mediacore/internal/manifest"
	"mediacore/internal/maptrack"
	"mediacore/internal/remux"
	"mediacore/internal/scan"
	"mediacore/internal/verify"
)

type processResult struct {
	caseReport *CaseReport
	manifest   *manifest.Manifest
	hash       string
}

func processMP4Case(name, work, input, output, manifestPath, cachePath string, p scan.Params, targetBitrate int64) (*processResult, error) {
	idx, rebuilt, err := scan.LoadOrBuild(input, cachePath, p)
	if err != nil {
		return nil, err
	}
	mappings, err := maptrack.Map(idx)
	if err != nil {
		return nil, err
	}
	if err := scan.SaveCache(cachePath, idx); err != nil {
		return nil, err
	}
	paceSamples(idx)

	if _, err := remux.WriteMP4(idx, mappings, idx.Samples, remux.Options{
		OutputPath: output, AllowedRoot: work, Resume: true,
	}); err != nil {
		return nil, err
	}
	metrics, err := verify.VerifyMP4(verify.Request{
		InputPath: input, OutputPath: output, CachePath: cachePath,
		TargetBitrate: targetBitrate, Mappings: mappings, Params: p,
	})
	if err != nil {
		return nil, err
	}

	m := manifest.New(name)
	m.Input = digestFile(input, idx.SourceSHA256)
	m.Output = digestFile(output, "")
	m.Index = manifest.IndexRecord{CachePath: cachePath, SourceSHA256: idx.SourceSHA256, IndexRebuilt: b2i(rebuilt)}
	m.Tracks = manifest.BuildTracks(idx, mappings)
	m.TimeRanges = manifest.BuildTimeRanges(metrics.OutputIndex.Samples)
	m.Metrics = metrics.Metrics
	m.Recovery.IndexRebuilt = b2i(rebuilt)
	m.Assertions = []string{
		"bitrate_delta_pct<=1",
		"video_psnr=+Inf/exact",
		"audio_diff_samples=0",
		"manifest_alignment_ms<=1",
	}
	if err := manifest.WriteJSON(manifestPath, m); err != nil {
		return nil, err
	}
	hash, err := manifest.CanonicalSHA256(m)
	if err != nil {
		return nil, err
	}
	m.Index.ManifestSHA256 = hash
	return &processResult{
		hash:     hash,
		manifest: m,
		caseReport: &CaseReport{
			Name: name, OK: true, ManifestPath: manifestPath, ManifestSHA256: hash,
			OutputPath: output, OutputSHA256: m.Output.SHA256,
			Recovery: m.Recovery, Metrics: m.Metrics,
		},
	}, nil
}

func digestFile(path, knownSHA string) manifest.FileDigest {
	st, _ := os.Stat(path)
	size := int64(0)
	if st != nil {
		size = st.Size()
	}
	sum := knownSHA
	if sum == "" {
		sum, _ = scan.SHA256File(path)
	}
	return manifest.FileDigest{Path: path, Size: size, SHA256: sum}
}

func paceSamples(idx *scan.Index) {
	if idx == nil || len(idx.Params.BandwidthBPS) == 0 {
		return
	}
	for i, s := range idx.Samples {
		bps := idx.Params.BandwidthBPS[i%len(idx.Params.BandwidthBPS)]
		pacer := clock.NewPacer(bps)
		_ = pacer.Charge(int(s.Size))
	}
}

func b2i(v bool) int {
	if v {
		return 1
	}
	return 0
}
