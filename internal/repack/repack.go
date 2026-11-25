// Package repack writes bounded sample chunks, checksums and assembled outputs.
package repack

import (
	"os"
	"path/filepath"

	"mediaflow/internal/index"
	"mediaflow/internal/plan"
	"mediaflow/internal/probe"
	"mediaflow/internal/safepath"
)

// Hooks allows tests and the terminal demo to inject interruption.
type Hooks struct {
	// AfterFirstSample is called after the first sample of the matching chunk is durable.
	AfterFirstSample func(chunkIndex int, tempPath string) error
}

// Options controls one item repackaging run.
type Options struct {
	WorkDir   string
	BatchKey  string
	ItemID    string
	InputPath string
	Report    *probe.Report
	Config    *plan.Config
	Hooks     Hooks
}

// Result records which chunks were reused versus rewritten.
type Result struct {
	CheckpointPath string `json:"checkpoint_path"`
	ChunkDir       string `json:"chunk_dir"`
	OutputPath     string `json:"output_path"`
	OutputDigest   string `json:"output_digest"`
	OutputBytes    int64  `json:"output_bytes"`
	ActualKbps     int    `json:"actual_kbps"`
	PlannedChunks  int    `json:"planned_chunks"`
	WrittenChunks  []int  `json:"written_chunks,omitempty"`
	SkippedChunks  []int  `json:"skipped_chunks,omitempty"`
	RolledBack     []int  `json:"rolled_back,omitempty"`
	OutputReused   bool   `json:"output_reused"`
}

// PlanUnits splits every supported track into ranges no larger than chunkSamples.
func PlanUnits(r *probe.Report, chunkSamples int) []index.Unit {
	if chunkSamples <= 0 {
		chunkSamples = 64
	}
	var units []index.Unit
	for _, t := range r.Tracks {
		if t == nil || len(t.Samples) == 0 {
			continue
		}
		for start := 0; start < len(t.Samples); start += chunkSamples {
			end := start + chunkSamples
			if end > len(t.Samples) {
				end = len(t.Samples)
			}
			units = append(units, index.Unit{TrackID: t.ID, StartSample: start, EndSample: end})
		}
	}
	return units
}

// Process runs or resumes the chunked repack operation.
func Process(opt Options) (*Result, error) {
	if opt.Report == nil || opt.Config == nil {
		return nil, os.ErrInvalid
	}
	cpPath, err := index.PathFor(opt.WorkDir, opt.BatchKey, opt.ItemID)
	if err != nil {
		return nil, err
	}
	stem := filepath.Base(cpPath)
	stem = stem[:len(stem)-len(".json")]
	chunkDir, err := safepath.Resolve(opt.WorkDir, "chunks", stem)
	if err != nil {
		return nil, err
	}
	outputPath, err := safepath.Resolve(opt.WorkDir, "outputs", stem+".mp4")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(chunkDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return nil, err
	}

	units := PlanUnits(opt.Report, opt.Config.ChunkSamples)
	cp, err := index.Load(cpPath)
	if err != nil {
		return nil, err
	}
	result := &Result{CheckpointPath: cpPath, ChunkDir: chunkDir, OutputPath: outputPath, PlannedChunks: len(units)}
	if cp == nil {
		cp = index.NewUnits(cpPath, opt.BatchKey, opt.ItemID,
			opt.Report.InputDigest, opt.Config.Digest, units)
		if err := cp.Save(); err != nil {
			return nil, err
		}
	} else if err := cp.CheckCompatibility(opt.BatchKey, opt.Report.InputDigest, opt.Config.Digest); err != nil {
		return nil, err
	} else {
		rolled := cp.RollbackIncomplete()
		result.RolledBack = rolled
		if err := cp.Save(); err != nil {
			return nil, err
		}
		for _, idx := range rolled {
			_ = removeChunkArtifacts(chunkDir, idx)
		}
	}

	input, err := os.Open(opt.InputPath)
	if err != nil {
		return nil, err
	}
	defer input.Close()

	for i := range cp.Chunks {
		rec := cp.Chunks[i]
		finalPath := chunkPath(chunkDir, i, false)
		if rec.Status == index.StatusComplete {
			ok, err := verifyChunkFile(finalPath, rec)
			if err != nil {
				return nil, err
			}
			if ok {
				result.SkippedChunks = append(result.SkippedChunks, i)
				continue
			}
		}
		if err := removeChunkArtifacts(chunkDir, i); err != nil {
			return nil, err
		}
		if err := cp.Begin(i); err != nil {
			return nil, err
		}
		written, newRec, err := writeChunk(input, opt.Report, cp.Chunks[i], chunkDir, opt.Hooks)
		if err != nil {
			return nil, err
		}
		if err := cp.MarkComplete(*newRec); err != nil {
			return nil, err
		}
		result.WrittenChunks = append(result.WrittenChunks, i)
		_ = written
	}

	if cp.Finalized {
		if ok, _ := verifyOutput(outputPath, cp.OutputDigest); ok {
			result.OutputReused = true
			result.OutputDigest = cp.OutputDigest
			info, _ := os.Stat(outputPath)
			if info != nil {
				result.OutputBytes = info.Size()
			}
			result.ActualKbps = actualKbps(opt.Report, result.OutputBytes)
			return result, nil
		}
	}
	digest, size, err := assembleOutput(outputPath, chunkDir, cp.Chunks)
	if err != nil {
		return nil, err
	}
	if err := cp.SetFinalOutput(digest); err != nil {
		return nil, err
	}
	result.OutputDigest = digest
	result.OutputBytes = size
	result.ActualKbps = actualKbps(opt.Report, size)
	return result, nil
}
