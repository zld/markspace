// Package batch runs the offline pipeline in fixed-size chunks and persists a
// checkpoint after every committed chunk. Resuming validates the checkpoint
// against the input bytes and the chunk geometry, then reprocesses only the
// chunks the checkpoint does not confirm.
package batch

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"tracecheck/internal/clock"
	"tracecheck/internal/ebml"
	"tracecheck/internal/index"
	"tracecheck/internal/mkv"
	"tracecheck/internal/serr"
)

// Options configures one batch run.
type Options struct {
	ChunkSize int // blocks committed per chunk; must be positive
	Resume    bool
}

// Chunk is one unit of committed work: every block in the chunk and the
// exact media bytes they reference, hashed together.
type Chunk struct {
	Index        int
	Records      []index.Record
	SegmentHash  string // hash of the chunk's fragment bytes
	CompletedEnd int64  // media time covered through this chunk, exclusive
}

// Summary is the deterministic result of a batch run.
type Summary struct {
	InputSHA256     string
	InputSize       int64
	ChunkSize       int
	TotalChunks     int
	CompletedChunks int
	Reprocessed     int // chunks recomputed on this run
	IndexDigest     string
	MetadataDigest  string
	SegmentHashes   []string
	Records         int
}

// Run executes the pipeline over an in-memory input. It parses, maps and
// indexes the input, splits the result into chunks and, when resume is on,
// reuses the appended prefix confirmed by prior checkpoint. The passed prior
// checkpoint (may be nil) is verified first.
func Run(data []byte, tb clock.Timebase, scaleLabel string, opts Options, prior *index.Checkpoint) (*Summary, error) {
	if opts.ChunkSize <= 0 {
		return nil, serr.New(serr.CodeInvalidArgs, "chunk size must be positive, got %d", opts.ChunkSize)
	}
	if err := tb.Validate(); err != nil {
		return nil, err
	}
	inputSum := sha256.Sum256(data)
	inputSHA := hex.EncodeToString(inputSum[:])

	if opts.Resume && prior != nil {
		if err := prior.VerifyAgainst(inputSHA, int64(len(data)), opts.ChunkSize); err != nil {
			return nil, err
		}
	}

	doc, err := ebml.Parse(data)
	if err != nil {
		return nil, err
	}
	blocks, err := mkv.ParseBlocks(doc)
	if err != nil {
		return nil, err
	}
	tracks, err := mkv.ParseTracks(doc)
	if err != nil {
		return nil, err
	}
	idx, err := index.Build(blocks, tb, tracks.TimestampScale, scaleLabel)
	if err != nil {
		return nil, err
	}

	chunks := splitChunks(idx, data, opts.ChunkSize)
	sum := &Summary{
		InputSHA256:    inputSHA,
		InputSize:      int64(len(data)),
		ChunkSize:      opts.ChunkSize,
		TotalChunks:    len(chunks),
		IndexDigest:    idx.Digest(),
		MetadataDigest: index.HashBytes([]byte(mkv.BuildMetadata(doc, tracks, blocks).Summary())),
		Records:        totalRecords(idx),
	}

	startChunk := 0
	if opts.Resume && prior != nil {
		if prior.IndexDigest != sum.IndexDigest {
			return nil, serr.New(serr.CodeCheckpointCorrupt,
				"checkpoint index digest %s does not match recomputed %s",
				prior.IndexDigest, sum.IndexDigest)
		}
		startChunk = prior.CompletedChunks
		if startChunk > len(chunks) {
			return nil, serr.New(serr.CodeCheckpointCorrupt,
				"checkpoint confirms %d chunks but only %d exist", startChunk, len(chunks))
		}
		sum.SegmentHashes = append(sum.SegmentHashes, prior.SegmentHashes...)
		sum.CompletedChunks = prior.CompletedChunks
	}
	for i := startChunk; i < len(chunks); i++ {
		sum.SegmentHashes = append(sum.SegmentHashes, chunks[i].SegmentHash)
		sum.CompletedChunks = i + 1
		sum.Reprocessed++
	}
	if sum.CompletedChunks > len(sum.SegmentHashes) {
		sum.CompletedChunks = len(sum.SegmentHashes)
	}
	return sum, nil
}

// splitChunks divides the ordered index records into chunks of at most size
// records, hashing each chunk's original fragment bytes.
func splitChunks(idx *index.Index, data []byte, size int) []Chunk {
	ordered := flatten(idx)
	var chunks []Chunk
	for start := 0; start < len(ordered); start += size {
		end := start + size
		if end > len(ordered) {
			end = len(ordered)
		}
		recs := ordered[start:end]
		h := make([]byte, 0, end-start)
		for _, r := range recs {
			if r.SampleStart < 0 || r.SampleEnd > int64(len(data)) || r.SampleStart > r.SampleEnd {
				continue
			}
			h = append(h, data[r.SampleStart:r.SampleEnd]...)
		}
		chunks = append(chunks, Chunk{
			Index:       len(chunks),
			Records:     recs,
			SegmentHash: index.HashBytes(h),
		})
	}
	return chunks
}

// flatten returns every record in the canonical (time, track, block) order.
func flatten(idx *index.Index) []index.Record {
	var out []index.Record
	for _, ti := range idx.Tracks {
		out = append(out, ti.Records...)
	}
	// Interleave tracks by time so chunks follow the real media order.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			if recordsBefore(out[j], out[j-1]) {
				out[j], out[j-1] = out[j-1], out[j]
				continue
			}
			break
		}
	}
	return out
}

func recordsBefore(a, b index.Record) bool {
	if a.TimeNanos != b.TimeNanos {
		return a.TimeNanos < b.TimeNanos
	}
	if a.Track != b.Track {
		return a.Track < b.Track
	}
	return a.BlockIndex < b.BlockIndex
}

func totalRecords(idx *index.Index) int {
	n := 0
	for _, ti := range idx.Tracks {
		n += len(ti.Records)
	}
	return n
}

// Checkpoint turns a completed summary into a checkpoint ready to persist.
func (s *Summary) Checkpoint(inputPath, metadataDigest string) *index.Checkpoint {
	return &index.Checkpoint{
		InputPath:       inputPath,
		InputSHA256:     s.InputSHA256,
		InputSize:       s.InputSize,
		CompletedOffset: s.InputSize,
		CompletedChunks: s.CompletedChunks,
		TotalChunks:     s.TotalChunks,
		IndexDigest:     s.IndexDigest,
		MetadataDigest:  s.MetadataDigest,
		SegmentHashes:   append([]string(nil), s.SegmentHashes...),
		ChunkSize:       s.ChunkSize,
		LastComplete:    s.CompletedChunks == s.TotalChunks,
	}
}

// String renders the deterministic demo summary line.
func (s *Summary) String() string {
	return fmt.Sprintf("chunks=%d/%d reprocessed=%d records=%d index=%s metadata=%s",
		s.CompletedChunks, s.TotalChunks, s.Reprocessed, s.Records,
		s.IndexDigest, s.MetadataDigest)
}
