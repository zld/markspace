package index

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/zld/continuity/internal/clock"
)

// Recovered describes the validated checkpoint state on disk.
type Recovered struct {
	// Committed are the chunk sequences that passed full validation, in order.
	Committed []uint64
	// Entries holds the decoded rows of every committed chunk.
	Entries []Entry
}

// Recover scans the store directory, validates each committed chunk against the
// expected fingerprint and its embedded checksum, and returns the work that can
// be skipped. A damaged chunk or a fingerprint change is reported with the
// offending sequence and the run is refused rather than partially resumed.
func (s *Store) Recover() (*Recovered, error) {
	matches, err := filepath.Glob(filepath.Join(s.dir, "chunk-*"+committedExt))
	if err != nil {
		return nil, err
	}
	seqs := make([]uint64, 0, len(matches))
	byPath := map[uint64]string{}
	for _, p := range matches {
		var seq uint64
		if _, err := fmt.Sscanf(filepath.Base(p), "chunk-%06d"+committedExt, &seq); err != nil {
			return nil, fmt.Errorf("%w: unreadable chunk name %q: %v",
				clock.ErrResumeMismatch, filepath.Base(p), err)
		}
		seqs = append(seqs, seq)
		byPath[seq] = p
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })

	rec := &Recovered{}
	for _, seq := range seqs {
		chunk, err := readChunk(byPath[seq])
		if err != nil {
			return nil, fmt.Errorf("%w: chunk %d: %v", clock.ErrResumeMismatch, seq, err)
		}
		if chunk.Fingerprint != s.fp {
			return nil, fmt.Errorf("%w: chunk %d input fingerprint changed",
				clock.ErrResumeMismatch, seq)
		}
		if chunk.Seq != seq {
			return nil, fmt.Errorf("%w: chunk %d header sequence %d",
				clock.ErrResumeMismatch, seq, chunk.Seq)
		}
		want := checksumEntries(chunk.Entries)
		if got := chunk.headerChecksum; got != want {
			return nil, fmt.Errorf("%w: chunk %d checksum mismatch",
				clock.ErrResumeMismatch, seq)
		}
		rec.Committed = append(rec.Committed, seq)
		rec.Entries = append(rec.Entries, chunk.Entries...)
		s.committed[seq] = true
	}
	return rec, nil
}

// IsCommitted reports whether a sequence was validated or written this run.
func (s *Store) IsCommitted(seq uint64) bool { return s.committed[seq] }

type decodedChunk struct {
	Chunk
	headerChecksum [32]byte
}

func readChunk(path string) (decodedChunk, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return decodedChunk{}, err
	}
	if len(data) < headerSize+4 {
		return decodedChunk{}, fmt.Errorf("file too short: %d bytes", len(data))
	}
	if string(data[0:8]) != chunkMagic {
		return decodedChunk{}, fmt.Errorf("bad magic %q", string(data[0:8]))
	}
	var fp Fingerprint
	copy(fp[:], data[8:40])
	seq := binary.BigEndian.Uint64(data[40:48])
	var checksum [32]byte
	copy(checksum[:], data[48:headerSize])
	count := binary.BigEndian.Uint32(data[headerSize : headerSize+4])

	out := decodedChunk{Chunk: Chunk{Seq: seq, Fingerprint: fp}, headerChecksum: checksum}
	pos := headerSize + 4
	for i := uint32(0); i < count; i++ {
		if pos+entryWireSize > len(data) {
			return decodedChunk{}, fmt.Errorf("entry %d truncated", i)
		}
		e, err := decodeEntry(data[pos : pos+entryWireSize])
		if err != nil {
			return decodedChunk{}, err
		}
		out.Entries = append(out.Entries, e)
		pos += entryWireSize
	}
	if pos != len(data) {
		return decodedChunk{}, fmt.Errorf("%d trailing bytes after %d entries",
			len(data)-pos, count)
	}
	return out, nil
}
