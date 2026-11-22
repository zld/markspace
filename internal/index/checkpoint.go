package index

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"mediasync/internal/mederr"
)

// chunkExt is the on-disk chunk file suffix.
const chunkExt = ".chunk"

// chunkHeader leads every chunk file and carries its own checksum.
type chunkHeader struct {
	Seq     int    `json:"seq"`
	Count   int    `json:"count"`
	SHA256  string `json:"sha256"` // hash of the contained record stream
}

// CheckpointStore durably stores index chunks inside one workspace
// directory. Writes are atomic: temp file + fsync + rename, so a crash can
// never leave a half-written chunk in place.
type CheckpointStore struct {
	dir string
}

// NewCheckpointStore opens (or creates) a checkpoint directory inside
// workspaceRoot; any path escaping the root is rejected.
func NewCheckpointStore(workspaceRoot, dir string) (*CheckpointStore, error) {
	if err := EnsureWithin(dir, workspaceRoot); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, mederr.New(mederr.CodeInternal,
			"cannot create checkpoint dir: "+err.Error())
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, mederr.New(mederr.CodePathViolation, err.Error())
	}
	return &CheckpointStore{dir: abs}, nil
}

// EnsureWithin rejects paths that escape root after cleaning symlinks.
// root must be an existing directory.
func EnsureWithin(target, root string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return mederr.New(mederr.CodePathViolation, err.Error())
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return mederr.New(mederr.CodePathViolation, err.Error())
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil || rel == ".." || filepath.IsAbs(rel) ||
		(len(rel) >= 3 && rel[:3] == ".."+string(os.PathSeparator)) {
		return mederr.New(mederr.CodePathViolation,
			fmt.Sprintf("path %q escapes workspace root %q", target, root))
	}
	return nil
}

func chunkPath(dir string, seq int) string {
	return filepath.Join(dir, fmt.Sprintf("chunk-%06d%s", seq, chunkExt))
}

// encodeRecord renders one record as a deterministic binary block.
func encodeRecord(r Record, buf *bytes.Buffer) {
	var b [8]byte
	put := func(v int64) {
		binary.BigEndian.PutUint64(b[:], uint64(v))
		buf.Write(b[:])
	}
	putStr := func(s string) {
		put(int64(len(s)))
		buf.WriteString(s)
	}
	putStr(r.Representation)
	putStr(r.Track)
	put(r.ObservedNS)
	put(r.CorrectedNS)
	put(int64(r.SegmentNumber))
	put(r.ByteOffset)
	put(r.InputSize)
}

// Commit atomically writes one chunk and marks it on the index.
func (s *CheckpointStore) Commit(ix *Index, seq int, records []Record) error {
	if len(records) == 0 {
		return mederr.New(mederr.CodeInternal, "refusing empty chunk commit")
	}
	var payload bytes.Buffer
	for _, r := range records {
		encodeRecord(r, &payload)
	}
	sum := sha256.Sum256(payload.Bytes())
	hdr := chunkHeader{Seq: seq, Count: len(records),
		SHA256: hex.EncodeToString(sum[:])}
	hdrBytes, err := json.Marshal(&hdr)
	if err != nil {
		return mederr.New(mederr.CodeInternal, err.Error())
	}
	var out bytes.Buffer
	var lenB [4]byte
	binary.BigEndian.PutUint32(lenB[:], uint32(len(hdrBytes)))
	out.Write(lenB[:])
	out.Write(hdrBytes)
	out.Write(payload.Bytes())

	final := chunkPath(s.dir, seq)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, out.Bytes(), 0o644); err != nil {
		return mederr.New(mederr.CodeInternal, "chunk write failed: "+err.Error())
	}
	if err := os.Rename(tmp, final); err != nil {
		return mederr.New(mederr.CodeInternal, "chunk rename failed: "+err.Error())
	}
	return ix.MarkChunkCommitted(seq)
}

// Load verifies every chunk in sequence order and replays valid records
// into a fresh index. A corrupt or missing chunk triggers rollback to the
// last contiguous valid checkpoint; the caller then re-processes the tail.
func Load(dir string, chunkRecords, limit int) (*Index, int, error) {
	ix, err := New(chunkRecords, limit)
	if err != nil {
		return nil, 0, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0, mederr.New(mederr.CodeInternal, err.Error())
	}
	var seqs []int
	for _, e := range entries {
		var seq int
		if _, err := fmt.Sscanf(e.Name(), "chunk-%06d"+chunkExt, &seq); err == nil {
			seqs = append(seqs, seq)
		}
	}
	sort.Ints(seqs)
	recovered := 0
	for _, seq := range seqs {
		recs, hdr, err := readChunk(chunkPath(dir, seq))
		if err != nil {
			// Corrupt chunk: roll back everything from here on.
			removeFrom(dir, seq)
			break
		}
		if hdr.Seq != seq || hdr.Count != len(recs) ||
			(seq > 0 && len(recs) != chunkRecords) {
			removeFrom(dir, seq)
			break
		}
		for _, r := range recs {
			if err := ix.Add(r); err != nil {
				removeFrom(dir, seq)
				return ix, recovered, err
			}
		}
		if err := ix.MarkChunkCommitted(seq); err != nil {
			return ix, recovered, err
		}
		recovered += len(recs)
	}
	return ix, recovered, nil
}

func readChunk(path string) ([]Record, chunkHeader, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, chunkHeader{}, mederr.New(mederr.CodeCheckpointCorrupt,
			"chunk unreadable: "+err.Error())
	}
	if len(data) < 4 {
		return nil, chunkHeader{}, mederr.New(mederr.CodeCheckpointCorrupt,
			"chunk too short")
	}
	hdrLen := int(binary.BigEndian.Uint32(data[:4]))
	if 4+hdrLen > len(data) {
		return nil, chunkHeader{}, mederr.New(mederr.CodeCheckpointCorrupt,
			"chunk header overruns file")
	}
	var hdr chunkHeader
	if err := json.Unmarshal(data[4:4+hdrLen], &hdr); err != nil {
		return nil, hdr, mederr.New(mederr.CodeCheckpointCorrupt,
			"chunk header invalid: "+err.Error())
	}
	payload := data[4+hdrLen:]
	sum := sha256.Sum256(payload)
	if hex.EncodeToString(sum[:]) != hdr.SHA256 {
		return nil, hdr, mederr.New(mederr.CodeCheckpointCorrupt,
			"chunk checksum mismatch")
	}
	if hdr.Count <= 0 || hdr.Seq < 0 {
		return nil, hdr, mederr.New(mederr.CodeCheckpointCorrupt,
			"chunk header fields invalid")
	}
	recs, err := decodeRecords(payload, hdr.Count)
	if err != nil {
		return nil, hdr, mederr.New(mederr.CodeCheckpointCorrupt, err.Error())
	}
	return recs, hdr, nil
}

func decodeRecords(payload []byte, count int) ([]Record, error) {
	recs := make([]Record, 0, count)
	r := bytes.NewReader(payload)
	readInt := func() (int64, error) {
		var b [8]byte
		_, err := r.Read(b[:])
		return int64(binary.BigEndian.Uint64(b[:])), err
	}
	readStr := func() (string, error) {
		n, err := readInt()
		if err != nil || n < 0 || int(n) > r.Len() {
			return "", mederr.New(mederr.CodeCheckpointCorrupt,
				"bad string length in chunk")
		}
		buf := make([]byte, n)
		if _, err := r.Read(buf); err != nil {
			return "", err
		}
		return string(buf), nil
	}
	for i := 0; i < count; i++ {
		var rec Record
		var err error
		if rec.Representation, err = readStr(); err != nil {
			return nil, err
		}
		if rec.Track, err = readStr(); err != nil {
			return nil, err
		}
		if rec.ObservedNS, err = readInt(); err != nil {
			return nil, err
		}
		if rec.CorrectedNS, err = readInt(); err != nil {
			return nil, err
		}
		if n, err := readInt(); err != nil {
			return nil, err
		} else {
			rec.SegmentNumber = int(n)
		}
		if rec.ByteOffset, err = readInt(); err != nil {
			return nil, err
		}
		if rec.InputSize, err = readInt(); err != nil {
			return nil, err
		}
		recs = append(recs, rec)
	}
	if r.Len() != 0 {
		return nil, mederr.New(mederr.CodeCheckpointCorrupt,
			"trailing bytes after chunk records")
	}
	return recs, nil
}

// removeFrom deletes the corrupt chunk and every higher-numbered chunk,
// implementing rollback to the last valid checkpoint.
func removeFrom(dir string, seq int) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		var s int
		if _, err := fmt.Sscanf(e.Name(), "chunk-%06d"+chunkExt, &s); err != nil {
			continue
		}
		if s >= seq {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
