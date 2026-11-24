package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mediarun/internal/fault"
	"mediarun/internal/journal"
	"mediarun/internal/verify"
)

// Run executes all stages with journaled resume and atomic commit.
func Run(opts Options) (*Result, error) {
	inDir, outDir, err := checkDirs(opts.InputDir, opts.OutDir)
	if err != nil {
		return nil, err
	}
	if err := cleanTemps(outDir); err != nil {
		return nil, err
	}
	inputDigest, manifests, err := digestInputs(inDir)
	if err != nil {
		return nil, err
	}
	paramDigest := journal.DigestBytes([]byte(
		fmt.Sprintf(`{"targetBitrate":%d}`, opts.TargetBitrate)))

	j := &journal.Journal{}
	if opts.Resume {
		if j, err = journal.Load(outDir); err != nil {
			return nil, err
		}
	}
	j.InputDigest = inputDigest
	j.ParamDigest = paramDigest

	runner := &stageRunner{inDir: inDir, outDir: outDir, j: j, opts: opts}

	tlRaw, err := runner.stage(StageManifest, VersionManifest, inputDigest, paramDigest,
		func() (any, error) { return buildTimeline(inDir, manifests) })
	if err != nil {
		return nil, err
	}
	timeline := tlRaw.(*Timeline)

	frRaw, err := runner.stage(StageFragments, VersionFragments, inputDigest, paramDigest,
		func() (any, error) { return processFragments(inDir, timeline) })
	if err != nil {
		return nil, err
	}
	fragments := frRaw.(*FragmentsResult)

	ixRaw, err := runner.stage(StageIndex, VersionIndex, inputDigest, paramDigest,
		func() (any, error) { return buildIndex(fragments) })
	if err != nil {
		return nil, err
	}
	ix := ixRaw.(*indexOutput)

	mtRaw, err := runner.stage(StageVerify, VersionVerify, inputDigest, paramDigest,
		func() (any, error) { return computeMetrics(opts.TargetBitrate, fragments, ix) })
	if err != nil {
		return nil, err
	}
	metrics := mtRaw.(*verify.Metrics)

	if bad := metrics.Check(verify.DefaultThresholds()); len(bad) > 0 {
		return nil, fault.New(fault.StageVerify,
			"thresholds not met: "+strings.Join(bad, "; "))
	}

	res, err := commitOutputs(outDir, j, timeline, fragments, ix, metrics)
	if err != nil {
		return nil, err
	}
	res.ReusedStages = j.ReusedCount()
	return res, nil
}

// stageRunner executes stages with journal reuse.
type stageRunner struct {
	inDir, outDir string
	j             *journal.Journal
	opts          Options
}

// stage returns the cached stage output when the journal key and the
// output digest match; otherwise it runs compute, persists the output
// atomically and records it in the journal.
func (r *stageRunner) stage(name string, version int, inputDigest, paramDigest string,
	compute func() (any, error)) (any, error) {
	key := journal.StageKey(inputDigest, paramDigest, name, version)
	path := filepath.Join(r.outDir, stagesDir, name+".json")
	if rec, ok := r.j.Lookup(name, key); ok {
		data, err := os.ReadFile(path)
		if err == nil && journal.DigestBytes(data) == rec.OutputDigest {
			out, err := decodeStage(name, data)
			if err == nil {
				rec.Reused = true
				return out, nil
			}
		}
		// Cached output missing or corrupt: recompute.
	}
	out, err := compute()
	if err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, fault.New(fault.StageJournal, "cannot encode stage output").Wrap(err)
	}
	if err := atomicWrite(path, data); err != nil {
		return nil, err
	}
	r.j.Put(&journal.StageRecord{
		Name: name, Version: version, Key: key,
		Status: journal.StatusVerified, OutputDigest: journal.DigestBytes(data),
	})
	if err := r.j.Save(r.outDir); err != nil {
		return nil, err
	}
	return out, nil
}

// decodeStage decodes a cached stage output into its concrete type.
func decodeStage(name string, data []byte) (any, error) {
	var out any
	switch name {
	case StageManifest:
		out = &Timeline{}
	case StageFragments:
		out = &FragmentsResult{}
	case StageIndex:
		out = &indexOutput{}
	case StageVerify:
		out = &verify.Metrics{}
	default:
		return nil, fmt.Errorf("unknown stage %q", name)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return nil, err
	}
	return out, nil
}
