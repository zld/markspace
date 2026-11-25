package selftest

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"mediacore/internal/fault"
	"mediacore/internal/manifest"
	"mediacore/internal/maptrack"
	"mediacore/internal/mediafix"
	"mediacore/internal/remux"
	"mediacore/internal/scan"
	"mediacore/internal/verify"
)

func runNormalCase(work string) (*CaseReport, error) {
	input := filepath.Join(work, "inputs", "normal.mp4")
	output := filepath.Join(work, "outputs", "normal.mp4")
	mf := filepath.Join(work, "manifests", "normal.json")
	cache := filepath.Join(work, "cache", "normal.json")
	if _, err := mediafix.WriteMaster(input, 0xA1); err != nil {
		return nil, err
	}
	params := scan.DefaultParams()

	// Warm the cache so the two audited reruns both represent unchanged inputs.
	if _, _, err := scan.LoadOrBuild(input, cache, params); err != nil {
		return nil, err
	}
	first, err := processMP4Case("normal", work, input, output, mf, cache, params, mediafix.TargetBitrate)
	if err != nil {
		return nil, err
	}
	second, err := processMP4Case("normal", work, input, output, mf, cache, params, mediafix.TargetBitrate)
	if err != nil {
		return nil, err
	}
	if first.hash != second.hash || first.caseReport.OutputSHA256 != second.caseReport.OutputSHA256 {
		return second.caseReport, fault.New(fault.Verification, "unchanged rerun manifest/output digest changed")
	}
	second.caseReport.Detail = "repeat-manifest-digest-match"
	return second.caseReport, nil
}

func runTruncatedCase(work string) (*CaseReport, error) {
	master := filepath.Join(work, "inputs", "truncated-master.mp4")
	input := filepath.Join(work, "inputs", "truncated.mp4")
	mf := filepath.Join(work, "manifests", "truncated.json")
	if _, err := mediafix.WriteMaster(master, 0xA2); err != nil {
		return nil, err
	}
	if err := copyFile(master, input); err != nil {
		return nil, err
	}
	if err := os.Truncate(input, 40); err != nil { // inside/right after mdat header at offset 24
		return nil, err
	}
	_, err := scan.BuildIndex(input, scan.DefaultParams())
	var fe *fault.Error
	if !errors.As(err, &fe) || fe.Code != fault.TruncatedData || fe.Offset != 24 {
		return &CaseReport{Name: "truncated", OK: false},
			fault.New(fault.Verification, "truncated fixture did not return truncated_data@24")
	}

	m := manifest.New("truncated")
	m.Input = digestFile(input, "")
	m.AddError(err)
	m.Assertions = []string{"deterministic_error=truncated_data", "offset=24"}
	if werr := manifest.WriteJSON(mf, m); werr != nil {
		return nil, werr
	}
	return &CaseReport{
		Name: "truncated", OK: true, Detail: "truncated_data@24",
		ExpectedErrorCode: string(fault.TruncatedData), ManifestPath: mf,
	}, nil
}

func runChangedCase(work string) (*CaseReport, error) {
	v1 := filepath.Join(work, "inputs", "changed-v1.mp4")
	v2 := filepath.Join(work, "inputs", "changed-v2.mp4")
	output := filepath.Join(work, "outputs", "changed.mp4")
	mf := filepath.Join(work, "manifests", "changed.json")
	cache := filepath.Join(work, "cache", "changed.json")
	if _, err := mediafix.WriteMaster(v1, 0xB1); err != nil {
		return nil, err
	}
	if _, err := mediafix.WriteMaster(v2, 0xC3); err != nil {
		return nil, err
	}
	params := scan.DefaultParams()
	if _, rebuilt, err := scan.LoadOrBuild(v1, cache, params); err != nil || !rebuilt {
		return nil, fault.New(fault.Verification, "initial changed-v1 index was not built")
	}
	if _, rebuilt, err := scan.LoadOrBuild(v1, cache, params); err != nil || rebuilt {
		return nil, fault.New(fault.Verification, "changed-v1 cache was not reused")
	}

	idx, rebuilt, err := scan.LoadOrBuild(v2, cache, params)
	if err != nil {
		return nil, err
	}
	if !rebuilt {
		return nil, fault.New(fault.Verification, "digest change did not rebuild index")
	}
	// An unchanged repeat after invalidation must reuse the new cache.
	if _, rebuiltAgain, err := scan.LoadOrBuild(v2, cache, params); err != nil || rebuiltAgain {
		return nil, fault.New(fault.Verification, "rebuilt changed-v2 cache was not reused")
	}

	mappings, err := maptrack.Map(idx)
	if err != nil {
		return nil, err
	}
	if err := scan.SaveCache(cache, idx); err != nil {
		return nil, err
	}
	if _, err := remux.WriteMP4(idx, mappings, idx.Samples, remux.Options{OutputPath: output, AllowedRoot: work, Resume: true}); err != nil {
		return nil, err
	}
	metrics, err := verify.VerifyMP4(verify.Request{
		InputPath: v2, OutputPath: output, CachePath: cache,
		TargetBitrate: mediafix.TargetBitrate, Mappings: mappings, Params: params,
	})
	if err != nil {
		return nil, err
	}
	m := manifest.New("changed")
	m.Input = digestFile(v2, idx.SourceSHA256)
	m.Output = digestFile(output, "")
	m.Index = manifest.IndexRecord{CachePath: cache, SourceSHA256: idx.SourceSHA256, IndexRebuilt: 1}
	m.Tracks = manifest.BuildTracks(idx, mappings)
	m.TimeRanges = manifest.BuildTimeRanges(metrics.OutputIndex.Samples)
	m.Metrics = metrics.Metrics
	m.Recovery.IndexRebuilt = 1
	m.Assertions = []string{"source_digest_change=index_rebuilt", "unchanged_rerun=cache_reused"}
	if err := manifest.WriteJSON(mf, m); err != nil {
		return nil, err
	}
	hash, _ := manifest.CanonicalSHA256(m)
	return &CaseReport{
		Name: "changed", OK: true, ManifestPath: mf, ManifestSHA256: hash,
		OutputPath: output, OutputSHA256: m.Output.SHA256, Recovery: m.Recovery, Metrics: m.Metrics,
		Detail: "stale-cache-rebuilt",
	}, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
