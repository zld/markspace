package demo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mediad/internal/archive"
	"mediad/internal/fixture"
	"mediad/internal/ingest"
	"mediad/internal/model"
	"mediad/internal/pipeline"
)

// Summary counts the run outcomes.
type Summary struct {
	Success      int
	Corrupt      int
	Restored     int
	Duplicates   int
	ChecksFailed int
}

// Report is the full result of one demo run.
type Report struct {
	Rows               []*Row
	Summary            Summary
	AllExpectationsMet bool
	ManifestPath       string
}

// Run executes the complete acceptance workflow under workDir.
func Run(ctx context.Context, workDir string, seed int64) (*Report, error) {
	rep := &Report{ManifestPath: filepath.Join(workDir, "archive", "manifest.json")}
	proc, err := NewProcessor(workDir)
	if err != nil {
		return nil, err
	}
	codecs := []string{ingest.CodecH264, ingest.CodecH265, ingest.CodecAV1}

	// 1. Three valid success samples.
	var validSamples []*fixture.Sample
	for _, codec := range codecs {
		s, err := fixture.Generate(fixture.DefaultParams(codec, seed))
		if err != nil {
			return nil, err
		}
		validSamples = append(validSamples, s)
		row, err := proc.ProcessSuccess(ctx, s)
		if err != nil {
			return nil, fmt.Errorf("success sample %s: %w", codec, err)
		}
		if fails := CheckAcceptance(row); len(fails) > 0 {
			rep.Summary.ChecksFailed += len(fails)
			row.Case = "success-FAIL:" + strings.Join(fails, ",")
		} else {
			rep.Summary.Success++
		}
		rep.Rows = append(rep.Rows, row)
	}

	// 2. Byte-identical manifest on a repeat run: capture bytes, reprocess the
	// same source (duplicate delivery), confirm the manifest is unchanged and
	// no second entry was appended.
	before, err := os.ReadFile(rep.ManifestPath)
	if err != nil {
		return nil, err
	}
	dupRow, err := proc.ProcessSuccess(ctx, validSamples[0])
	if err != nil {
		return nil, err
	}
	rep.Summary.Duplicates++
	rep.Rows = append(rep.Rows, dupRow)
	after, err := os.ReadFile(rep.ManifestPath)
	if err != nil {
		return nil, err
	}
	if string(before) != string(after) {
		rep.Summary.ChecksFailed++
		rep.Rows = append(rep.Rows, failRow("manifest changed bytes on duplicate delivery"))
	}
	if err := archive.EnsureNoDuplicateEntries(rep.ManifestPath); err != nil {
		rep.Summary.ChecksFailed++
		rep.Rows = append(rep.Rows, failRow("duplicate manifest entry after rerun"))
	}

	// 3. Corruption variants for each codec. Each must report a code/offset
	// and end with exit code 2, never producing a success manifest.
	entriesBefore := len(proc.manifest.Entries)
	for _, s := range validSamples {
		variants, err := fixture.Corrupt(s)
		if err != nil {
			return nil, err
		}
		for _, v := range variants {
			row := proc.ProcessCorrupt(v)
			if row.ErrorCode == "" || row.ExitCode != model.ExitCorrupt {
				rep.Summary.ChecksFailed++
			} else {
				rep.Summary.Corrupt++
			}
			rep.Rows = append(rep.Rows, row)
		}
	}
	if len(proc.manifest.Entries) != entriesBefore {
		rep.Summary.ChecksFailed++
		rep.Rows = append(rep.Rows, failRow("corrupt sample appended a manifest entry"))
	}

	// 4. Interruption residue and resume: a source whose ingest committed but
	// normalize was interrupted; rerun must clean the residue, finish the
	// stages and keep the committed ingest digest stable.
	if err := runInterruptionResume(ctx, proc, workDir, seed, rep); err != nil {
		return nil, err
	}

	rep.AllExpectationsMet = rep.Summary.ChecksFailed == 0
	return rep, nil
}

// runInterruptionResume creates residue, records the committed ingest digest,
// reruns and asserts the digest is unchanged and no duplicate manifest entry
// appears.
func runInterruptionResume(ctx context.Context, proc *Processor, workDir string, seed int64, rep *Report) error {
	s, err := fixture.Generate(fixture.DefaultParams(ingest.CodecH264, seed+1))
	if err != nil {
		return err
	}
	idx, err := ingest.ValidateIndex(s.Codec, s.Index.Entries, int64(len(s.Stream)))
	if err != nil {
		return err
	}
	fp, err := ingest.FingerprintBytes(s.Codec, s.Stream, idx)
	if err != nil {
		return err
	}
	if _, _, err := proc.writeSource(fp.SourceSHA, s.Stream); err != nil {
		return err
	}
	workRoot := filepath.Join(workDir, "pipeline")
	st, err := proc.engine.LoadState(fp.SourceSHA, s.Codec)
	if err != nil {
		return err
	}
	residue := proc.engine.StageDir(fp.SourceSHA, pipeline.StageNormalize)
	firstRun := st.NextPending() != ""

	if firstRun {
		// First invocation for this source: leave an interrupted normalize.
		if err := fixture.WriteInterruptionResidue(workRoot, fp.SourceSHA, s.Codec); err != nil {
			return err
		}
		if _, err := os.Stat(residue); err != nil {
			return fmt.Errorf("expected interruption residue: %w", err)
		}
	}

	// Snapshot the committed ingest digest before the (re)run.
	st, err = proc.engine.LoadState(fp.SourceSHA, s.Codec)
	if err != nil {
		return err
	}
	beforeDigest := ingestDigest(st)

	row, err := proc.ProcessSuccess(ctx, s)
	if err != nil {
		return fmt.Errorf("resume processing: %w", err)
	}
	rep.Summary.Restored++
	rep.Rows = append(rep.Rows, row)

	// The previously committed ingest digest must be unchanged.
	st2, err := proc.engine.LoadState(fp.SourceSHA, s.Codec)
	if err != nil {
		return err
	}
	afterDigest := ingestDigest(st2)
	if beforeDigest == "" || beforeDigest != afterDigest {
		rep.Summary.ChecksFailed++
		rep.Rows = append(rep.Rows, failRow("committed ingest digest changed after resume"))
	}
	if _, err := os.Stat(residue); err == nil {
		rep.Summary.ChecksFailed++
		rep.Rows = append(rep.Rows, failRow("interruption residue not cleaned after resume"))
	}
	if err := archive.EnsureNoDuplicateEntries(rep.ManifestPath); err != nil {
		rep.Summary.ChecksFailed++
		rep.Rows = append(rep.Rows, failRow("duplicate manifest entry after restore"))
	}
	return nil
}

// failRow builds a synthetic row recording a failed expectation.
func failRow(msg string) *Row {
	return &Row{
		Case:        "check-failed",
		Stage:       "validation",
		ExitCode:    model.ExitGeneric,
		ErrorCode:   model.ErrStageState,
		ErrorOffset: -1,
		SourceSHA:   msg,
	}
}

// ingestDigest returns the recorded digest of the committed ingest stage, or
// "" when the stage is not committed yet.
func ingestDigest(st *pipeline.State) string {
	rec := st.Stages[pipeline.StageIngest]
	if rec == nil || rec.State != pipeline.StateCommitted {
		return ""
	}
	for _, d := range rec.Artifacts {
		return d
	}
	return ""
}
