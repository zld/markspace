package batch

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"mediabatch/internal/checkpoint"
	"mediabatch/internal/clock"
	"mediabatch/internal/fault"
	"mediabatch/internal/fingerprint"
	"mediabatch/internal/index"
	"mediabatch/internal/recovery"
	"mediabatch/internal/report"
	"mediabatch/internal/scan"
)

// Mode selects how existing state is treated.
type Mode int

const (
	// ModeDemo resets derived state and runs from scratch.
	ModeDemo Mode = iota
	// ModeResume keeps committed state and continues where it stopped.
	ModeResume
)

// Execute runs the pipeline.
func Execute(opts Options, mode Mode) (*Run, error) {
	if opts.ChunkSize <= 0 {
		return nil, fault.NewErr(fault.CodeCheckpoint, fault.Locator{},
			"chunk size must be positive, got %d", opts.ChunkSize)
	}
	if mode == ModeDemo {
		if err := ResetState(opts.WorkDir); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(opts.WorkDir, 0o755); err != nil {
		return nil, fault.NewErr(fault.CodeIndexCommit, fault.Locator{File: opts.WorkDir}, "work dir: %v", err)
	}

	units, parseIssues, err := CollectUnits(opts.InputDir, opts.MaxSegmentBytes)
	if err != nil {
		return nil, err
	}
	run := &Run{Issues: append(fault.List(nil), parseIssues...)}

	outcomes, publishable, duplicates := scanUnits(units, opts.MaxSegmentBytes, run)
	run.Outcomes = outcomes
	run.Counts.Duplicates = duplicates

	chunks, err := checkpoint.Plan(len(units), opts.ChunkSize)
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

	store, err := index.Open(opts.WorkDir)
	if err != nil {
		return nil, err
	}
	state, err := checkpoint.Load(opts.WorkDir)
	if err != nil {
		return nil, err
	}
	// On resume the chunk size is fixed by the interrupted run: a different
	// size would shift every boundary and invalidate all committed digests.
	if mode == ModeResume && state.ChunkSize > 0 && state.ChunkSize != opts.ChunkSize {
		run.Counts.Warnings = append(run.Counts.Warnings,
			fmt.Sprintf("chunk size %d differs from the checkpoint's %d", opts.ChunkSize, state.ChunkSize))
	}
	state.ChunkSize = opts.ChunkSize
	state.Chunks = chunks
	if err := state.Save(opts.WorkDir); err != nil {
		return nil, err
	}

	plan := recovery.Build(opts.WorkDir, store, recoveryInputs(chunks, outcomes, publishable))
	run.RecoveryIssues = plan.Issues
	run.Plan = plan
	run.Counts.Reused = plan.ReusedArtifacts
	run.Counts.Recomputed = plan.RecomputedSegments
	run.Counts.Revalidated = plan.RevalidatedSegments

	for _, cp := range plan.Chunks {
		chunk := chunks[cp.Chunk]
		switch cp.Decision {
		case recovery.Reuse:
			state.Finish(cp.Chunk)
			if err := state.Save(opts.WorkDir); err != nil {
				return run, err
			}
		case recovery.Adopt:
			adopted, err := adoptChunk(opts.WorkDir, store, cp, outcomes, publishable)
			if err != nil {
				return run, err
			}
			run.AdoptedArtifacts += adopted
			state.Finish(cp.Chunk)
			if err := state.Save(opts.WorkDir); err != nil {
				return run, err
			}
		default:
			done, err := publishChunk(opts, store, state, chunk, outcomes, publishable, run)
			if err != nil {
				return run, err
			}
			if !done {
				run.Crashed = true
				run.CrashPoint = opts.CrashPoint
				finalizeRun(run)
				return run, nil
			}
		}
	}
	finalizeRun(run)
	return run, nil
}

// scanUnits validates every unit and assigns dedup keys. Units that fail
// validation, or whose content matches another unit at a different time or
// size, are never made publishable.
func scanUnits(units []Unit, maxBytes int64, run *Run) ([]Outcome, []bool, int) {
	reqs := make([]scan.Request, len(units))
	for i, u := range units {
		reqs[i] = scan.Request{
			Manifest: u.Manifest, Representation: u.Representation, Ref: u.Ref,
			Path: u.Path, InitPath: u.InitPath, Start: u.Start, End: u.End,
			Discontinuity: u.Discontinuity, Expected: u.Expect, MaxBytes: maxBytes,
		}
	}
	results := scan.ScanAll(reqs)
	outcomes := make([]Outcome, len(units))
	publishable := make([]bool, len(units))
	seen := map[string]bool{}
	duplicates := 0
	var records []fingerprint.Record
	for i, res := range results {
		run.Issues = append(run.Issues, res.Issues...)
		o := Outcome{Unit: units[i], Result: res}
		if !res.OK() {
			outcomes[i] = o
			continue
		}
		rec := fingerprint.FromResult(units[i].Manifest, units[i].Representation,
			units[i].Ref, units[i].Start, units[i].End, res.Hash, res.Size)
		o.Key = rec.Key()
		o.Artifact = artifactPathFor(o.Key, units[i].Ref)
		if seen[o.Key] {
			o.Duplicate = true
			duplicates++
		} else {
			seen[o.Key] = true
		}
		outcomes[i] = o
		publishable[i] = true
		records = append(records, rec)
	}
	ix := fingerprint.Build(records)
	run.Issues = append(run.Issues, ix.Issues...)
	// Content that matches but differs in time range or size is a timing
	// anomaly, not a duplicate: isolate both sides rather than merging.
	rejected := map[string]bool{}
	for _, r := range ix.Rejected {
		rejected[r.Key()] = true
	}
	for i := range outcomes {
		if outcomes[i].Key == "" || !rejected[outcomes[i].Key] {
			continue
		}
		publishable[i] = false
		outcomes[i].Duplicate = false
		if !seen[outcomes[i].Key] {
			duplicates--
		}
	}
	return outcomes, publishable, duplicates
}

// publishChunk writes artifacts first, then commits the index. It returns
// done=false when fault injection asked the process to stop after artifacts
// were written, leaving an uncommitted chunk for recovery to adopt.
func publishChunk(opts Options, store *index.Store, state *checkpoint.State, chunk checkpoint.Chunk, outcomes []Outcome, publishable []bool, run *Run) (bool, error) {
	state.Begin(chunk.Index)
	if err := state.Save(opts.WorkDir); err != nil {
		return false, err
	}
	keys := make([]string, 0, chunk.Count())
	entries := make([]index.Entry, 0, chunk.Count())
	for i := range outcomes {
		o := &outcomes[i]
		if !publishable[i] || o.Artifact == "" {
			continue
		}
		if o.Chunk != chunk.Index {
			continue
		}
		target := filepath.Join(opts.WorkDir, filepath.FromSlash(o.Artifact))
		if _, err := os.Stat(target); err == nil {
			// Already on disk from an earlier interrupted attempt: reuse it.
			run.Counts.Reused++
			o.Reused = true
		} else if err := copyFile(o.Unit.Path, target); err != nil {
			return false, fault.NewErr(fault.CodeArtifactMissing, fault.Locator{File: o.Unit.Path},
				"write artifact %s: %v", o.Artifact, err)
		} else {
			run.NewArtifacts++
			o.Published = true
		}
		keys = append(keys, o.Key)
		entries = append(entries, entryFor(*o))
	}
	if opts.CrashPoint == crashAfterArtifact {
		return false, nil
	}
	// A revalidated chunk may no longer contain a previously published key
	// (for example, a segment now fails its checksum). Drop those stale index
	// entries so the index never claims a quarantined artifact is published.
	if prev, ok := store.Chunk(chunk.Index); ok {
		store.RemoveEntriesExcept(prev.Keys, keys)
	}
	for _, e := range entries {
		store.Put(e)
	}
	store.CommitChunk(chunk.Index, chunk.InputDigest, keys)
	if err := store.Save(opts.WorkDir); err != nil {
		return false, err
	}
	if opts.CrashPoint == crashAfterCommit {
		return false, nil
	}
	state.Finish(chunk.Index)
	if err := state.Save(opts.WorkDir); err != nil {
		return false, err
	}
	return true, nil
}

// adoptChunk records artifacts that exist on disk without an index commit. The
// existing bytes are hashed and adopted; nothing is rewritten or republished.
func adoptChunk(workDir string, store *index.Store, cp recovery.ChunkPlan, outcomes []Outcome, publishable []bool) (int, error) {
	adopted := 0
	keys := make([]string, 0, len(outcomes))
	for i := range outcomes {
		o := &outcomes[i]
		if !publishable[i] || o.Artifact == "" || o.Chunk != cp.Chunk {
			continue
		}
		target := filepath.Join(workDir, filepath.FromSlash(o.Artifact))
		sum, size, err := fingerprint.HashFile(target)
		if err != nil {
			return adopted, fault.NewErr(fault.CodeArtifactMissing, fault.Locator{File: target},
				"adopt artifact: %v", err)
		}
		e := entryFor(*o)
		e.ContentSHA = sum
		e.Size = size
		store.Put(e)
		keys = append(keys, o.Key)
		adopted++
	}
	store.CommitChunk(cp.Chunk, cp.InputDigest, keys)
	if err := store.Save(workDir); err != nil {
		return adopted, err
	}
	return adopted, nil
}

func entryFor(o Outcome) index.Entry {
	return index.Entry{
		Key: o.Key, ContentSHA: o.Result.Hash, Size: o.Result.Size,
		Start: o.Unit.Start, End: o.Unit.End,
		Manifest: o.Unit.Manifest, Representation: o.Unit.Representation,
		Segment: o.Unit.Ref, Artifact: o.Artifact, Chunk: o.Chunk,
	}
}

func recoveryInputs(chunks []checkpoint.Chunk, outcomes []Outcome, publishable []bool) []recovery.ChunkInput {
	byChunk := map[int]*recovery.ChunkInput{}
	for i := range outcomes {
		o := &outcomes[i]
		if !publishable[i] || o.Artifact == "" {
			continue
		}
		in, ok := byChunk[o.Chunk]
		if !ok {
			c := chunks[o.Chunk]
			in = &recovery.ChunkInput{
				Chunk: c.Index, Start: c.First, End: c.Last, InputDigest: c.InputDigest,
			}
			byChunk[o.Chunk] = in
		}
		dup := false
		for _, a := range in.Artifacts {
			if a.Rel == o.Artifact {
				dup = true
				break
			}
		}
		if !dup {
			in.Artifacts = append(in.Artifacts, recovery.ArtifactRef{Rel: o.Artifact, Key: o.Key})
		}
	}
	out := make([]recovery.ChunkInput, 0, len(chunks))
	for i := range chunks {
		if in, ok := byChunk[i]; ok {
			out = append(out, *in)
			continue
		}
		out = append(out, recovery.ChunkInput{
			Chunk: chunks[i].Index, Start: chunks[i].First, End: chunks[i].Last,
			InputDigest: chunks[i].InputDigest,
		})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Chunk < out[b].Chunk })
	return out
}

func chunkIndexFor(chunks []checkpoint.Chunk, total int) []int {
	out := make([]int, total)
	for _, c := range chunks {
		for i := c.First; i <= c.Last && i < total; i++ {
			out[i] = c.Index
		}
	}
	return out
}

func finalizeRun(run *Run) {
	run.Issues.Sort()
	run.Counts.Segments = len(run.Outcomes)
	run.Counts.Anomalies = len(run.Issues)
	run.Counts.Chunks = len(run.Chunks)
	run.Counts.NewArtifacts = run.NewArtifacts
	run.Counts.Published = run.publishableCount()
}

// publishableCount reports segments that produced an artifact (new or reused).
func (r *Run) publishableCount() int {
	n := 0
	for _, o := range r.Outcomes {
		if o.Key != "" && o.Artifact != "" {
			n++
		}
	}
	return n
}

// WriteReport writes report.json and report.txt to the work dir, optionally
// restricted to the input time range [from, to]. A nil bound is open-ended.
func (r *Run) WriteReport(workDir string, from, to *clock.Time) error {
	rep := report.Build(r.Issues, r.Counts, nil)
	rep.Filter(from, to)
	// Each entry carries the recovery action taken and a final validation
	// summary naming the chunk and the artifact state it ended in.
	rep.Annotate(func(e report.Entry) string {
		return r.verifySummary(e)
	})
	r.Report = rep
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := rep.WriteJSON(&buf); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(workDir, "report.json"), buf.Bytes(), 0o644); err != nil {
		return err
	}
	var text bytes.Buffer
	if err := rep.WriteText(&text); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(workDir, "report.txt"), text.Bytes(), 0o644)
}

// verifySummary describes the final state of the artifact that would carry the
// flagged segment, or states that none exists because the item was quarantined.
func (r *Run) verifySummary(e report.Entry) string {
	for _, o := range r.Outcomes {
		if o.Unit.Ref != e.Segment || o.Unit.Representation != e.Representation {
			continue
		}
		if !o.Unit.Start.Equal(e.Start) {
			continue
		}
		if o.Result.OK() && o.Artifact != "" {
			state := "present"
			if o.Published {
				state = "written"
			} else if o.Reused {
				state = "reused"
			}
			return fmt.Sprintf("chunk=%d artifact=%s %s", o.Chunk, o.Artifact, state)
		}
		return fmt.Sprintf("chunk=%d status=%s quarantined", o.Chunk, o.Result.Status)
	}
	return "not scheduled"
}

func artifactPathFor(key, ref string) string {
	sum := sha256.Sum256([]byte(key))
	name := hex.EncodeToString(sum[:])
	ext := filepath.Ext(filepath.Base(ref))
	if ext == "" {
		ext = ".bin"
	}
	return filepath.ToSlash(filepath.Join("artifacts", name[:2], name+ext))
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".artifact-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, dst)
}

// ResetState removes every derived file from a work dir while preserving the
// generated input tree.
func ResetState(workDir string) error {
	for _, p := range []string{"index.json", "checkpoint.json", "report.json", "report.txt", "verify.json"} {
		if err := os.Remove(filepath.Join(workDir, p)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return os.RemoveAll(filepath.Join(workDir, "artifacts"))
}

var _ = fmt.Sprintf
