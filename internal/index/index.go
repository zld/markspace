// Package index builds the random-access keyframe index. Records are
// committed in fixed-size chunks (default 256) with per-chunk SHA-256
// checksums; checkpoints only advance once a whole chunk is durable.
package index

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"mediasync/internal/mederr"
)

// MaxBatchRecords caps one indexing batch per the operating limits.
const MaxBatchRecords = 1_000_000

// DefaultChunkRecords is the mandated checkpoint chunk size.
const DefaultChunkRecords = 256

// Record is one keyframe entry: representation, track, pre/post-repair
// instants, source segment and byte offset.
type Record struct {
	Representation string
	Track          string
	ObservedNS     int64
	CorrectedNS    int64
	SegmentNumber  int
	ByteOffset     int64
	InputSize      int64
}

// Digest is the deterministic summary of a complete index.
type Digest struct {
	Records int
	SHA256  string
}

// Index is an in-memory, append-only keyframe index with chunk callbacks.
type Index struct {
	chunkRecords int
	limit        int
	records      []Record
	chunkSeqs    []int // sequence numbers already committed
}

// New validates the chunk size and batch limit.
func New(chunkRecords, limit int) (*Index, error) {
	if chunkRecords <= 0 {
		return nil, mederr.New(mederr.CodeInvalidManifest,
			"chunk-records must be positive")
	}
	if limit <= 0 || limit > MaxBatchRecords {
		return nil, mederr.New(mederr.CodeLimitExceeded,
			fmt.Sprintf("batch limit %d out of range (1..%d)",
				limit, MaxBatchRecords))
	}
	return &Index{chunkRecords: chunkRecords, limit: limit}, nil
}

// Add appends a record after validating the byte range invariant.
func (ix *Index) Add(r Record) error {
	if len(ix.records) >= ix.limit {
		return mederr.New(mederr.CodeLimitExceeded,
			fmt.Sprintf("index batch limit %d reached", ix.limit))
	}
	if r.ByteOffset < 0 || (r.InputSize > 0 && r.ByteOffset >= r.InputSize) {
		return mederr.New(mederr.CodeInvalidManifest,
			fmt.Sprintf("record offset %d outside input size %d",
				r.ByteOffset, r.InputSize),
			mederr.WithTrack(r.Representation), mederr.WithOffset(r.ByteOffset))
	}
	ix.records = append(ix.records, r)
	return nil
}

// PendingChunks returns full chunks ready to commit, in sequence order.
func (ix *Index) PendingChunks() [][]Record {
	committed := len(ix.chunkSeqs) * ix.chunkRecords
	var out [][]Record
	for start := committed; start+ix.chunkRecords <= len(ix.records); start += ix.chunkRecords {
		out = append(out, ix.records[start:start+ix.chunkRecords])
	}
	return out
}

// MarkChunkCommitted records that chunk seq (0-based) is durably stored.
func (ix *Index) MarkChunkCommitted(seq int) error {
	want := len(ix.chunkSeqs)
	if seq != want {
		return mederr.New(mederr.CodeCheckpointCorrupt,
			fmt.Sprintf("chunk seq %d out of order, expected %d", seq, want))
	}
	ix.chunkSeqs = append(ix.chunkSeqs, seq)
	return nil
}

// Tail returns the not-yet-committed records for replay after a resume.
func (ix *Index) Tail() []Record {
	committed := len(ix.chunkSeqs) * ix.chunkRecords
	return append([]Record(nil), ix.records[committed:]...)
}

// Lookup returns the nearest keyframe whose CorrectedNS is not later than
// targetNS, or false when no such keyframe exists.
func (ix *Index) Lookup(targetNS int64) (Record, bool) {
	idx := sort.Search(len(ix.records), func(i int) bool {
		return ix.records[i].CorrectedNS > targetNS
	}) - 1
	if idx < 0 {
		return Record{}, false
	}
	return ix.records[idx], true
}

// Records returns a defensive copy of all records in insertion order.
func (ix *Index) Records() []Record {
	return append([]Record(nil), ix.records...)
}

// ChunkRecords exposes the configured chunk size.
func (ix *Index) ChunkRecords() int { return ix.chunkRecords }

// CommittedChunks reports how many chunks have durable checkpoints.
func (ix *Index) CommittedChunks() int { return len(ix.chunkSeqs) }

// Hash computes the canonical SHA-256 over the exact record stream, so a
// resumed run and a full run over identical input produce the same digest.
func Hash(records []Record) string {
	h := sha256.New()
	var buf [8]byte
	writeInt := func(v int64) {
		for i := 7; i >= 0; i-- {
			buf[i] = byte(v)
			v >>= 8
		}
		h.Write(buf[:])
	}
	writeStr := func(s string) {
		writeInt(int64(len(s)))
		h.Write([]byte(s))
	}
	writeInt(int64(len(records)))
	for _, r := range records {
		writeStr(r.Representation)
		writeStr(r.Track)
		writeInt(r.ObservedNS)
		writeInt(r.CorrectedNS)
		writeInt(int64(r.SegmentNumber))
		writeInt(r.ByteOffset)
		writeInt(r.InputSize)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Digest returns the canonical summary of the current record stream.
func (ix *Index) Digest() Digest {
	recs := ix.Records()
	return Digest{Records: len(recs), SHA256: Hash(recs)}
}
