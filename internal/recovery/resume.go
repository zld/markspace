// Package recovery reconciles the artifact tree with the committed index on
// startup. The rule is conservative: an artifact that exists but has no index
// commit is adopted from its existing bytes (never rewritten), and a committed
// chunk whose artifact is missing is recomputed from that chunk onward.
// Re-running a completed work dir therefore publishes nothing new.
package recovery

import (
	"os"
	"path/filepath"
	"sort"

	"mediabatch/internal/fault"
	"mediabatch/internal/index"
)

// Decision classifies the work left for one chunk.
type Decision string

const (
	// Reuse means committed and present: nothing to do.
	Reuse Decision = "reuse"
	// Adopt means the artifacts exist but the index commit was lost; record
	// the original artifacts instead of publishing them again.
	Adopt Decision = "adopt"
	// Recompute means the commit exists but at least one artifact is gone.
	Recompute Decision = "recompute"
	// Revalidate means the chunk inputs changed; scan the chunk again.
	Revalidate Decision = "revalidate"
	// Scan means the chunk was never processed.
	Scan Decision = "scan"
)

// ArtifactRef is one expected artifact and its on-disk state.
type ArtifactRef struct {
	// Rel is the path relative to the work dir, using forward slashes.
	Rel string
	// Key is the index key of the segment that owns this artifact.
	Key string
	// Exists reports whether the file is present on disk.
	Exists bool
	// Committed reports whether the index already records the key.
	Committed bool
}

// ChunkInput is what the caller knows about a chunk before recovery runs.
type ChunkInput struct {
	Chunk       int
	Start, End  int
	InputDigest string
	Artifacts   []ArtifactRef
}

// ChunkPlan is the reconciliation result for one chunk.
type ChunkPlan struct {
	Chunk         int
	Start, End    int
	InputDigest   string
	IndexedDigest string
	Committed     bool
	Decision      Decision
	Artifacts     []ArtifactRef
	// Missing lists expected artifacts absent from disk.
	Missing []string
	// Uncommitted lists present artifacts with no index entry.
	Uncommitted []string
	// Reused lists present, committed artifacts that need no write.
	Reused []string
}

// Count returns the number of segments in the chunk.
func (c ChunkPlan) Count() int {
	if c.End < c.Start {
		return 0
	}
	return c.End - c.Start + 1
}

// Plan summarises the whole recovery decision.
type Plan struct {
	Chunks      []ChunkPlan
	ResumeChunk int
	// ReusedArtifacts counts artifacts that will not be rewritten.
	ReusedArtifacts int
	// AdoptedArtifacts counts artifacts recorded from disk without rewriting.
	AdoptedArtifacts int
	// RecomputedSegments/RevalidatedSegments count affected segments.
	RecomputedSegments  int
	RevalidatedSegments int
	// DuplicatePublish must stay zero: recovery never writes an artifact that
	// already exists.
	DuplicatePublish int
	Issues           fault.List
}

// Build reconciles store state with the disk.
func Build(workDir string, store *index.Store, inputs []ChunkInput) Plan {
	p := Plan{ResumeChunk: 0}
	sorted := append([]ChunkInput(nil), inputs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Chunk < sorted[j].Chunk })

	for _, in := range sorted {
		cp := ChunkPlan{Chunk: in.Chunk, Start: in.Start, End: in.End, InputDigest: in.InputDigest}
		if cs, ok := store.Chunk(in.Chunk); ok {
			cp.Committed = cs.Committed
			cp.IndexedDigest = cs.InputDigest
		}
		for _, a := range in.Artifacts {
			a.Exists = false
			if _, err := os.Stat(filepath.Join(workDir, filepath.FromSlash(a.Rel))); err == nil {
				a.Exists = true
			}
			if _, ok := store.Lookup(a.Key); ok {
				a.Committed = true
			}
			if !a.Exists {
				cp.Missing = append(cp.Missing, a.Rel)
			}
			if a.Exists && !a.Committed {
				cp.Uncommitted = append(cp.Uncommitted, a.Rel)
			}
			cp.Artifacts = append(cp.Artifacts, a)
		}
		cp.Decision = decide(cp)
		p.Chunks = append(p.Chunks, cp)
	}

	p.ResumeChunk = len(p.Chunks)
	for i := range p.Chunks {
		cp := &p.Chunks[i]
		switch cp.Decision {
		case Reuse:
			p.ReusedArtifacts += len(cp.Artifacts)
		case Adopt:
			p.AdoptedArtifacts += len(cp.Artifacts)
			p.ReusedArtifacts += len(cp.Artifacts)
		case Recompute:
			p.RecomputedSegments += cp.Count()
		case Revalidate:
			p.RevalidatedSegments += cp.Count()
		}
		if cp.Decision != Reuse && cp.Chunk < p.ResumeChunk {
			p.ResumeChunk = cp.Chunk
		}
	}
	for _, cp := range p.Chunks {
		if cp.Decision == Reuse {
			continue
		}
		p.Issues = append(p.Issues, issueFor(cp))
	}
	p.Issues.Sort()
	return p
}

func decide(cp ChunkPlan) Decision {
	switch {
	case cp.Committed && cp.IndexedDigest != cp.InputDigest:
		return Revalidate
	case cp.Committed && len(cp.Missing) > 0:
		return Recompute
	case cp.Committed:
		return Reuse
	case len(cp.Artifacts) > 0 && len(cp.Missing) == 0:
		return Adopt
	case len(cp.Missing) < len(cp.Artifacts):
		return Recompute
	default:
		return Scan
	}
}

func issueFor(cp ChunkPlan) fault.Issue {
	switch cp.Decision {
	case Adopt:
		return fault.Issue{
			Code: fault.CodeIndexCommit, Severity: fault.SeverityWarning, Action: fault.ActionReused,
			Message: "artifacts exist without an index commit; adopted without republishing",
			Source: fault.Source{
				Manifest: "chunk",
				Segment:  chunkLabel(cp.Chunk),
			},
		}
	case Recompute:
		return fault.Issue{
			Code: fault.CodeArtifactMissing, Severity: fault.SeverityError, Action: fault.ActionRepaired,
			Message: "artifact missing for a committed chunk; recomputing from this chunk",
			Source: fault.Source{
				Manifest: "chunk",
				Segment:  chunkLabel(cp.Chunk),
			},
		}
	case Revalidate:
		return fault.Issue{
			Code: fault.CodeCheckpoint, Severity: fault.SeverityWarning, Action: fault.ActionRepaired,
			Message: "chunk inputs changed; revalidating affected block",
			Source: fault.Source{
				Manifest: "chunk",
				Segment:  chunkLabel(cp.Chunk),
			},
		}
	default:
		return fault.Issue{
			Code: fault.CodeCheckpoint, Severity: fault.SeverityWarning, Action: fault.ActionRecorded,
			Message: "chunk has not been processed yet",
			Source: fault.Source{
				Manifest: "chunk",
				Segment:  chunkLabel(cp.Chunk),
			},
		}
	}
}

func chunkLabel(n int) string { return "chunk-" + itoa(n) }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// Blocked reports whether a decision requires re-running the chunk.
func (d Decision) Blocked() bool {
	return d == Recompute || d == Revalidate || d == Scan
}
