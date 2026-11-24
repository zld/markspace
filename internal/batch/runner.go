package batch

import (
	"os"
	"path/filepath"
	"slices"

	"mediad/internal/clock"
	"mediad/internal/ingest"
	"mediad/internal/mederr"
)

// MaxChunks bounds one offline batch run.
const MaxChunks = ingest.MaxChunks

// PlanChunk is one half-open [StartNs,EndNs) work unit.
type PlanChunk struct {
	Index   int
	Span    clock.Span
	RelPath string
}

// RenderFunc writes one chunk atomically to tmpPath. It must not rename the
// file; the runner verifies and commits it.
type RenderFunc func(chunk PlanChunk, tmpPath string) error

// VerifyFunc performs format-level validation after rendering or before
// reusing an existing output.
type VerifyFunc func(path string, chunk PlanChunk) error

// RunReport records deterministic progress and recovery actions.
type RunReport struct {
	IndexRebuilt   bool  `json:"index_rebuilt"`
	Planned        int   `json:"planned"`
	TotalCommitted int   `json:"chunks_committed"`
	NewlyCommitted int   `json:"chunks_newly_committed"`
	Reused         int   `json:"chunks_reused"`
	Redone         []int `json:"chunks_redone"`
	Truncated      []int `json:"truncated_chunks"`
}

// Runner executes a chunk plan with checkpoint semantics.
type Runner struct {
	Dir         string
	Fingerprint string
	Render      RenderFunc
	Verify      VerifyFunc
}

// PlanChunks splits total into chunkNs-aligned half-open intervals. The final
// chunk may be shorter. Inputs are bounded and invalid spans fail explicitly.
func PlanChunks(total clock.Span, chunkNs int64) ([]PlanChunk, error) {
	if chunkNs <= 0 {
		return nil, mederr.New(mederr.CatLimit, "batch", total.Start, total.End, false,
			"chunk duration must be positive")
	}
	if total.Start < 0 || total.End <= total.Start || total.Duration() > ingest.MaxDurationNanos {
		return nil, mederr.New(mederr.CatLimit, "batch", total.Start, total.End, false,
			"invalid total span %+v", total)
	}
	count := int((total.Duration() + chunkNs - 1) / chunkNs)
	if count > MaxChunks {
		return nil, mederr.New(mederr.CatLimit, "batch", total.Start, total.End, false,
			"chunk count %d exceeds limit %d", count, MaxChunks)
	}
	out := make([]PlanChunk, 0, count)
	for i := 0; i < count; i++ {
		start := total.Start + int64(i)*chunkNs
		end := start + chunkNs
		if end > total.End {
			end = total.End
		}
		out = append(out, PlanChunk{Index: i, Span: clock.Span{Start: start, End: end}})
	}
	return out, nil
}

// Run executes all planned chunks and checkpoints after every commit.
func (r *Runner) Run(plan []PlanChunk) (*Index, RunReport, error) {
	var rep RunReport
	rep.Planned = len(plan)
	if r.Render == nil {
		return nil, rep, mederr.New(mederr.CatIO, "batch", -1, -1, false, "runner has no render function")
	}
	if len(plan) == 0 {
		return nil, rep, mederr.New(mederr.CatLimit, "batch", -1, -1, false, "empty chunk plan")
	}
	if len(plan) > MaxChunks {
		return nil, rep, mederr.New(mederr.CatLimit, "batch", -1, -1, false,
			"chunk count %d exceeds limit %d", len(plan), MaxChunks)
	}
	if err := os.MkdirAll(filepath.Join(r.Dir, "chunks"), 0o755); err != nil {
		return nil, rep, mederr.New(mederr.CatIO, "batch", -1, -1, false, "create chunks dir: %v", err)
	}
	idx, loadRep, err := LoadIndex(r.Dir, r.Fingerprint)
	if err != nil {
		return nil, rep, err
	}
	rep.IndexRebuilt = loadRep.Rebuilt

	byIndex := map[int]ChunkRecord{}
	for _, c := range idx.Chunks {
		byIndex[c.Index] = c
	}

	for _, pc := range plan {
		if pc.Index < 0 || pc.RelPath == "" {
			return nil, rep, mederr.New(mederr.CatLimit, "batch", pc.Span.Start, pc.Span.End, false,
				"chunk %d has no output path", pc.Index)
		}
		finalPath := filepath.Join(r.Dir, pc.RelPath)
		if !filepath.IsLocal(pc.RelPath) {
			return nil, rep, mederr.New(mederr.CatLimit, "batch", pc.Span.Start, pc.Span.End, false,
				"chunk path %q escapes output directory", pc.RelPath)
		}

		if rec, ok := byIndex[pc.Index]; ok && r.recordReusable(rec, pc, finalPath) {
			rep.Reused++
			continue
		} else if ok {
			rep.Redone = append(rep.Redone, pc.Index)
			if _, statErr := os.Stat(finalPath); statErr == nil {
				sum, _, hashErr := HashFile(finalPath)
				verifyErr := error(nil)
				if r.Verify != nil {
					verifyErr = r.Verify(finalPath, pc)
				}
				if hashErr != nil || verifyErr != nil || sum != rec.SHA256 {
					rep.Truncated = append(rep.Truncated, pc.Index)
				}
			}
		} else if _, statErr := os.Stat(finalPath); statErr == nil && r.Verify != nil {
			if verr := r.Verify(finalPath, pc); verr != nil {
				rep.Truncated = append(rep.Truncated, pc.Index)
				rep.Redone = append(rep.Redone, pc.Index)
			}
		}

		if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
			return nil, rep, mederr.New(mederr.CatIO, "batch", pc.Span.Start, pc.Span.End, false,
				"create chunk dir: %v", err)
		}
		tmpPath := finalPath + ".tmp"
		_ = os.Remove(tmpPath)
		if err := r.Render(pc, tmpPath); err != nil {
			_ = os.Remove(tmpPath)
			return nil, rep, mederr.New(mederr.CatIO, "batch", pc.Span.Start, pc.Span.End, false,
				"render chunk %d: %v", pc.Index, err)
		}
		if r.Verify != nil {
			if err := r.Verify(tmpPath, pc); err != nil {
				_ = os.Remove(tmpPath)
				return nil, rep, mederr.New(mederr.CatChecksum, "batch", pc.Span.Start, pc.Span.End, false,
					"chunk %d verification failed: %v", pc.Index, err)
			}
		}
		sum, size, err := HashFile(tmpPath)
		if err != nil {
			_ = os.Remove(tmpPath)
			return nil, rep, mederr.New(mederr.CatChecksum, "batch", pc.Span.Start, pc.Span.End, false,
				"hash chunk %d: %v", pc.Index, err)
		}
		if err := os.Rename(tmpPath, finalPath); err != nil {
			_ = os.Remove(tmpPath)
			return nil, rep, mederr.New(mederr.CatIO, "batch", pc.Span.Start, pc.Span.End, false,
				"commit chunk %d: %v", pc.Index, err)
		}
		rec := ChunkRecord{
			Index:       pc.Index,
			StartNs:     pc.Span.Start,
			EndNs:       pc.Span.End,
			File:        pc.RelPath,
			Bytes:       size,
			SHA256:      sum,
			Fingerprint: r.Fingerprint,
		}
		byIndex[pc.Index] = rec
		idx.Fingerprint = r.Fingerprint
		idx.Chunks = recordsSorted(byIndex)
		if err := idx.Save(r.Dir); err != nil {
			return nil, rep, err
		}
		rep.NewlyCommitted++
	}
	idx.Chunks = recordsSorted(byIndex)
	rep.TotalCommitted = len(idx.Chunks)
	return idx, rep, nil
}

func (r *Runner) recordReusable(rec ChunkRecord, pc PlanChunk, finalPath string) bool {
	if rec.Fingerprint != r.Fingerprint || rec.File != pc.RelPath ||
		rec.StartNs != pc.Span.Start || rec.EndNs != pc.Span.End {
		return false
	}
	sum, size, err := HashFile(finalPath)
	if err != nil || size != rec.Bytes || sum != rec.SHA256 {
		return false
	}
	if r.Verify != nil {
		if err := r.Verify(finalPath, pc); err != nil {
			return false
		}
	}
	return true
}

func recordsSorted(m map[int]ChunkRecord) []ChunkRecord {
	out := make([]ChunkRecord, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b ChunkRecord) int { return a.Index - b.Index })
	return out
}

// CheckLateOverlap rejects a late span that overlaps any committed chunk.
// It is read-only: the caller's checkpoint file is not modified.
func CheckLateOverlap(idx *Index, track string, span clock.Span) error {
	if span.End <= span.Start {
		return mederr.New(mederr.CatTimeline, track, span.Start, span.End, false,
			"late fragment has invalid span")
	}
	for _, c := range idx.Chunks {
		cs := clock.Span{Start: c.StartNs, End: c.EndNs}
		if cs.Overlaps(span) {
			return mederr.New(mederr.CatOverlap, track, span.Start, span.End, false,
				"late fragment overlaps committed chunk %d [%dms,%dms); checkpoint preserved",
				c.Index, c.StartNs/1e6, c.EndNs/1e6)
		}
	}
	return nil
}
