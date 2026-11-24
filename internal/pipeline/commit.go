package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mediarun/internal/fault"
	"mediarun/internal/index"
	"mediarun/internal/journal"
	"mediarun/internal/repair"
	"mediarun/internal/verify"
)

const (
	reportFile = "report.json"
	indexFile  = "index.json"
	digestFile = "output.digest"
)

// commitOutputs builds the provenance report and atomically commits the
// final outputs. Every committed file is replaced via temp-file + rename,
// so an interrupted commit never leaves a half-written result.
func commitOutputs(outDir string, j *journal.Journal, tl *Timeline,
	fr *FragmentsResult, ix *index.Index, m *verify.Metrics) (*Result, error) {

	indexBytes, err := json.MarshalIndent(ix, "", "  ")
	if err != nil {
		return nil, fault.New(fault.StageCommit, "cannot encode index").Wrap(err)
	}
	repairDigest, err := repairMapDigest(fr)
	if err != nil {
		return nil, err
	}
	rep := &Report{
		InputDigest:     j.InputDigest,
		ParamDigest:     j.ParamDigest,
		Manifests:       tl.Manifests,
		Fragments:       fragmentDigests(fr),
		RepairMapDigest: repairDigest,
		IndexDigest:     journal.DigestBytes(indexBytes),
		Metrics:         *m,
	}
	reportBytes, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return nil, fault.New(fault.StageCommit, "cannot encode report").Wrap(err)
	}

	indexPath := filepath.Join(outDir, indexFile)
	if err := atomicWrite(indexPath, indexBytes); err != nil {
		return nil, err
	}
	reportPath := filepath.Join(outDir, reportFile)
	if err := atomicWrite(reportPath, reportBytes); err != nil {
		return nil, err
	}
	// Best-effort plain digest pointer; the authoritative digest is
	// recomputed from report.json by VerifyCommitted.
	sum := journal.DigestBytes(reportBytes)
	if err := atomicWrite(filepath.Join(outDir, digestFile), []byte(sum+"\n")); err != nil {
		return nil, err
	}

	j.MarkCommitted()
	if err := j.Save(outDir); err != nil {
		return nil, err
	}
	return &Result{Metrics: *m, Report: rep, OutputDigest: sum}, nil
}

// fragmentDigests returns the de-duplicated, path-sorted source fragment
// list for the provenance trail.
func fragmentDigests(fr *FragmentsResult) []FileDigest {
	seen := map[string]FileDigest{}
	for _, tr := range fr.Tracks {
		for _, s := range tr.Segments {
			seen[s.Path] = FileDigest{Path: s.Path, SHA256: s.Digest}
		}
	}
	out := make([]FileDigest, 0, len(seen))
	for _, fd := range seen {
		out = append(out, fd)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// repairMapDigest hashes the complete per-track repair mapping.
func repairMapDigest(fr *FragmentsResult) (string, error) {
	maps := map[string][]repair.Entry{}
	for _, tr := range fr.Tracks {
		if len(tr.Repairs) > 0 {
			maps[tr.ID] = tr.Repairs
		}
	}
	data, err := json.Marshal(maps) // encoding/json sorts map keys
	if err != nil {
		return "", fault.New(fault.StageCommit, "cannot encode repair map").Wrap(err)
	}
	return journal.DigestBytes(data), nil
}

// atomicWrite writes data to a temp file in the same directory, fsyncs
// it and renames it over the target, protecting committed results.
func atomicWrite(path string, data []byte) error {
	tmp := filepath.Join(filepath.Dir(path), ".tmp-"+filepath.Base(path))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fault.New(fault.StageCommit, "cannot write temp file").At(tmp, -1).Wrap(err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fault.New(fault.StageCommit, "cannot write temp file").At(tmp, -1).Wrap(err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fault.New(fault.StageCommit, "cannot fsync temp file").At(tmp, -1).Wrap(err)
	}
	if err := f.Close(); err != nil {
		return fault.New(fault.StageCommit, "cannot close temp file").At(tmp, -1).Wrap(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fault.New(fault.StageCommit, "cannot commit output").At(path, -1).Wrap(err)
	}
	return nil
}

// VerifyCommitted reloads a committed report, checks that the current
// input bytes still match its provenance digest, verifies the committed
// index digest and evaluates the acceptance thresholds. It returns the
// report together with the stable output digest (sha256 of report.json).
func VerifyCommitted(inputDir, outDir string) (*Result, error) {
	inAbs, err := filepath.Abs(inputDir)
	if err != nil {
		return nil, fault.New(fault.StageInput, "bad input path").Wrap(err)
	}
	if st, err := os.Stat(inAbs); err != nil || !st.IsDir() {
		return nil, fault.New(fault.StageInput, "input is not a directory").At(inAbs, -1)
	}
	outAbs, err := filepath.Abs(outDir)
	if err != nil {
		return nil, fault.New(fault.StageInput, "bad output path").Wrap(err)
	}
	reportPath := filepath.Join(outAbs, reportFile)
	reportBytes, err := os.ReadFile(reportPath)
	if err != nil {
		return nil, fault.New(fault.StageVerify,
			"no committed report; run 'process' first").At(reportPath, -1).Wrap(err)
	}
	rep := &Report{}
	if err := json.Unmarshal(reportBytes, rep); err != nil {
		return nil, fault.New(fault.StageVerify, "corrupt report").At(reportPath, -1).Wrap(err)
	}
	// Input provenance must still match the bytes on disk.
	inputDigest, _, err := digestInputs(inAbs)
	if err != nil {
		return nil, err
	}
	if inputDigest != rep.InputDigest {
		return nil, fault.New(fault.StageVerify,
			"input digest differs from committed provenance; rerun process without stale reuse")
	}
	// Committed index must match its recorded digest.
	indexBytes, err := os.ReadFile(filepath.Join(outAbs, indexFile))
	if err != nil {
		return nil, fault.New(fault.StageVerify, "missing committed index").
			At(filepath.Join(outAbs, indexFile), -1).Wrap(err)
	}
	if journal.DigestBytes(indexBytes) != rep.IndexDigest {
		return nil, fault.New(fault.StageVerify, "index digest mismatch; committed output is tampered or corrupt")
	}
	if bad := rep.Metrics.Check(verify.DefaultThresholds()); len(bad) > 0 {
		return nil, fault.New(fault.StageVerify,
			"thresholds not met: "+strings.Join(bad, "; "))
	}
	return &Result{
		Metrics:      rep.Metrics,
		Report:       rep,
		OutputDigest: journal.DigestBytes(reportBytes),
	}, nil
}
