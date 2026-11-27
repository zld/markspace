package batch

import (
	"encoding/json"
	"os"
	"path/filepath"

	"mediabatch/internal/checkpoint"
	"mediabatch/internal/fault"
	"mediabatch/internal/index"
	"mediabatch/internal/recovery"
	"mediabatch/internal/report"
)

// Inspect re-derives the unit list from the input tree and builds the recovery
// plan without writing anything. The CLI uses it for dry summaries.
func Inspect(workDir, inputDir string, maxBytes int64) (*Run, error) {
	units, parseIssues, err := CollectUnits(inputDir, maxBytes)
	if err != nil {
		return nil, err
	}
	run := &Run{Issues: append(fault.List(nil), parseIssues...)}
	outcomes, publishable, duplicates := scanUnits(units, maxBytes, run)
	run.Outcomes = outcomes
	run.Counts.Duplicates = duplicates
	size := chunkSizeFromState(workDir)
	chunks, err := checkpoint.Plan(len(units), size)
	if err != nil {
		return nil, err
	}
	ids := make([]checkpoint.Identity, len(units))
	for i, u := range units {
		ids[i] = u.Identity(outcomes[i].Result.Hash)
	}
	checkpoint.AssignDigests(chunks, ids)
	run.Chunks = chunks
	chunkOf := chunkIndexFor(chunks, len(units))
	for i := range outcomes {
		outcomes[i].Chunk = chunkOf[i]
	}
	store, err := index.Open(workDir)
	if err != nil {
		return nil, err
	}
	plan := recovery.Build(workDir, store, recoveryInputs(chunks, outcomes, publishable))
	run.Plan = plan
	run.RecoveryIssues = plan.Issues
	finalizeRun(run)
	return run, nil
}

func chunkSizeFromState(workDir string) int {
	st, err := checkpoint.Load(workDir)
	if err != nil || st.ChunkSize <= 0 {
		return 3
	}
	return st.ChunkSize
}

// ManifestParseFailed reports whether any manifest could not be parsed. A
// parse failure makes the run exit non-zero even when other manifests produced
// a usable report.
func (r *Run) ManifestParseFailed() bool {
	for _, i := range r.Issues {
		if i.Code == fault.CodeParse && i.Severity == fault.SeverityError {
			return true
		}
	}
	return false
}

// ResumeSummary is printed by the resume command.
type ResumeSummary struct {
	CheckpointLine   string
	ChunkSize        int
	ResumeChunk      int
	TotalChunks      int
	Reused           int
	Adopted          int
	Recomputed       int
	Revalidated      int
	NewArtifacts     int
	DuplicatePublish int
	Decisions        []recovery.ChunkPlan
}

// SummariseResume builds the resume summary from a run and its plan.
func (r *Run) SummariseResume(workDir string) ResumeSummary {
	st, _ := checkpoint.Load(workDir)
	s := ResumeSummary{
		ResumeChunk:      r.Plan.ResumeChunk,
		TotalChunks:      len(r.Chunks),
		Reused:           r.Plan.ReusedArtifacts,
		Adopted:          r.Plan.AdoptedArtifacts,
		Recomputed:       r.Plan.RecomputedSegments,
		Revalidated:      r.Plan.RevalidatedSegments,
		NewArtifacts:     r.NewArtifacts,
		DuplicatePublish: r.Plan.DuplicatePublish,
		Decisions:        r.Plan.Chunks,
	}
	if st != nil {
		s.CheckpointLine = st.Summarize()
		s.ChunkSize = st.ChunkSize
	}
	return s
}

// LoadReport reads the persisted report.json.
func LoadReport(workDir string) (*report.Report, error) {
	path := filepath.Join(workDir, "report.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fault.NewErr(fault.CodeInputMissing, fault.Locator{File: path}, "read report: %v", err)
	}
	var rep report.Report
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, fault.NewErr(fault.CodeIndexCorrupt, fault.Locator{File: path}, "decode report: %v", err)
	}
	return &rep, nil
}

// VerifyResult is the verify command's machine output.
type VerifyResult struct {
	Entries          int
	SortedViolations int
	MissingSource    int
	Digest           string
	IndexEntries     int
}

// Verify recomputes the report digest from the persisted report and checks the
// index is readable. It never mutates state.
func Verify(workDir string) (VerifyResult, error) {
	var vr VerifyResult
	rep, err := LoadReport(workDir)
	if err != nil {
		return vr, err
	}
	// Check the persisted order before touching anything: sorting first would
	// make the violation count trivially zero on every report.
	vr.Entries = len(rep.Entries)
	vr.SortedViolations = rep.SortedViolations()
	vr.MissingSource = rep.MissingSource()
	vr.Digest = report.DigestOf(rep.Entries)
	store, err := index.Open(workDir)
	if err != nil {
		return vr, err
	}
	vr.IndexEntries = len(store.Entries)
	return vr, nil
}
