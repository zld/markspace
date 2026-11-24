package core

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"mediad/internal/model"
)

// Fingerprint is the deterministic identity of one chunk. Identical decoded
// content across representations hashes identically regardless of which
// representation produced it.
type Fingerprint struct {
	ChunkID string `json:"chunk_id"`
	Hash    string `json:"hash"`
	PayloadBytes int `json:"payload_bytes"`
}

// HashChunk builds a canonical hash over the ordered frame payloads. Only
// frame bytes and their order enter the hash; timestamps are excluded so
// cross-representation duplicates still collide.
func HashChunk(c Chunk) Fingerprint {
	h := sha256.New()
	total := 0
	for _, f := range c.Frames {
		h.Write(f.Payload)
		total += len(f.Payload)
	}
	sum := h.Sum(nil)
	return Fingerprint{
		ChunkID: c.ID,
		Hash:    hex.EncodeToString(sum),
		PayloadBytes: total,
	}
}

// DedupeRecord marks one chunk's role after cross-representation matching.
type DedupeRecord struct {
	Hash           string `json:"hash"`
	PrimaryChunkID string `json:"primary_chunk_id"`
	PrimaryRep     string `json:"primary_representation"`
	DuplicateOf    string `json:"duplicate_of,omitempty"`
	Kept           bool   `json:"kept"`
	BlockedMerge   bool   `json:"blocked_merge"`
	Sources        []Source `json:"sources"`
}

// Source preserves where a hash was observed, even for duplicate records.
type Source struct {
	Representation string         `json:"representation"`
	ChunkID        string         `json:"chunk_id"`
	Start          model.PlayTime `json:"start_ns"`
	End            model.PlayTime `json:"end_ns"`
}

// DedupeReport groups records by hash. blockedReps lists representations that
// must never merge even when content matches (manifest misalignment).
type DedupeReport struct {
	Records          []DedupeRecord `json:"records"`
	DuplicateCount   int            `json:"deduplicated_count"`
}

// DedupChunks performs deterministic cross-representation deduplication.
// Iteration order is fixed (representation id, then chunk start) so the
// primary selection is reproducible run after run.
func DedupChunks(chunksByRep map[string][]Chunk, blockedReps map[string]bool) *DedupeReport {
	type item struct {
		rep   string
		chunk Chunk
		fp    Fingerprint
	}
	var items []item
	reps := make([]string, 0, len(chunksByRep))
	for r := range chunksByRep {
		reps = append(reps, r)
	}
	sort.Strings(reps)
	for _, r := range reps {
		for _, c := range chunksByRep[r] {
			items = append(items, item{r, c, HashChunk(c)})
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].fp.Hash != items[j].fp.Hash {
			return items[i].fp.Hash < items[j].fp.Hash
		}
		if items[i].rep != items[j].rep {
			return items[i].rep < items[j].rep
		}
		return items[i].chunk.Start < items[j].chunk.Start
	})
	rep := &DedupeReport{}
	byHash := map[string]int{} // hash -> primary index in rep.Records
	for _, it := range items {
		src := Source{Representation: it.rep, ChunkID: it.chunk.ID,
			Start: it.chunk.Start, End: it.chunk.End}
		idx, exists := byHash[it.fp.Hash]
		if !exists {
			rep.Records = append(rep.Records, DedupeRecord{
				Hash: it.fp.Hash, PrimaryChunkID: it.chunk.ID,
				PrimaryRep: it.rep, Kept: true,
				BlockedMerge: blockedReps[it.rep], Sources: []Source{src},
			})
			byHash[it.fp.Hash] = len(rep.Records) - 1
			continue
		}
		rec := &rep.Records[idx]
		rec.Sources = append(rec.Sources, src)
		// Misalignment is a property of the content pair: once any
		// representation of this hash is blocked, every occurrence stays
		// independent and nothing becomes a duplicate.
		if rec.BlockedMerge || blockedReps[it.rep] {
			rec.BlockedMerge = true
			rep.Records = append(rep.Records, DedupeRecord{
				Hash: it.fp.Hash, PrimaryChunkID: it.chunk.ID, PrimaryRep: it.rep,
				Kept: true, BlockedMerge: true, Sources: []Source{src},
			})
			continue
		}
		rep.Records = append(rep.Records, DedupeRecord{
			Hash: it.fp.Hash, PrimaryChunkID: rec.PrimaryChunkID,
			PrimaryRep: rec.PrimaryRep, DuplicateOf: rec.PrimaryChunkID,
			Kept: false, Sources: []Source{src},
		})
		rep.DuplicateCount++
	}
	return rep
}
