// Package checkpoint persists resumable kernel state. Each checkpoint records
// the input offset consumed, the watermark, the drift state, the ordered
// output digest so far and a digest of the checkpoint body itself. A resume
// verifies the prefix checksum of the log and falls back to the newest valid
// checkpoint, reporting why the fallback happened.
package checkpoint

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"mediasync/internal/model"
)

// FormatVersion is the checkpoint record schema version.
const FormatVersion = 1

// Record is one checkpoint line. Body fields are covered by Digest.
type Record struct {
	Version    int     `json:"version"`
	Index      int64   `json:"index"`
	EventSeq   int64   `json:"event_seq"`
	InputByte  int64   `json:"input_byte"`
	Watermark  int64   `json:"watermark_ns"`
	DriftPPM   float64 `json:"drift_ppm"`
	DriftSlope float64 `json:"drift_slope"`
	// OutputDigest is the digest of all released output up to this point.
	OutputDigest string `json:"output_digest"`
	// Summary carries the counters needed to continue reporting.
	Lost          int64 `json:"lost"`
	Recovered     int64 `json:"recovered"`
	LateRecompute int64 `json:"late_recomputed"`
	// Payload is opaque kernel state, already base64 encoded by the caller.
	Payload string `json:"payload"`
	Digest  string `json:"digest"`
}

// digestFields are the fields covered by the record digest, in fixed order.
func (r Record) digest() string {
	h := sha256.New()
	fmt.Fprintf(h, "%d|%d|%d|%d|%d|%.12f|%.12f|%s|%d|%d|%d|%s",
		r.Version, r.Index, r.EventSeq, r.InputByte, r.Watermark,
		r.DriftPPM, r.DriftSlope, r.OutputDigest, r.Lost, r.Recovered,
		r.LateRecompute, r.Payload)
	return hex.EncodeToString(h.Sum(nil))
}

// Store appends checkpoint records to a single log file.
type Store struct {
	dir string
	// MaxRecords bounds the log; older records are dropped on compaction.
	MaxRecords int
}

// NewStore validates the working directory.
func NewStore(dir string, maxRecords int) (*Store, error) {
	if dir == "" {
		return nil, model.NewError(model.CodeCheckpointMissing, model.Loc{}, "checkpoint directory must be set")
	}
	if maxRecords <= 0 {
		return nil, model.NewError(model.CodeCheckpointCorrupt, model.Loc{File: dir}, "MaxRecords must be positive, got %d", maxRecords)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, model.NewError(model.CodeCheckpointMissing, model.Loc{File: dir}, "create checkpoint directory").Wrap(err)
	}
	return &Store{dir: dir, MaxRecords: maxRecords}, nil
}

// Dir returns the store directory.
func (s *Store) Dir() string { return s.dir }

func (s *Store) path() string { return filepath.Join(s.dir, "checkpoints.jsonl") }

// Append writes a record, computing its digest first, and syncs the file.
func (s *Store) Append(r Record) (Record, error) {
	r.Version = FormatVersion
	r.Digest = r.digest()
	if r.Digest == "" {
		return r, model.NewError(model.CodeCheckpointCorrupt, model.Loc{File: s.path()}, "empty digest")
	}
	f, err := os.OpenFile(s.path(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return r, model.NewError(model.CodeCheckpointMissing, model.Loc{File: s.path()}, "open checkpoint log").Wrap(err)
	}
	defer f.Close()
	buf, err := json.Marshal(r)
	if err != nil {
		return r, model.NewError(model.CodeCheckpointCorrupt, model.Loc{File: s.path()}, "encode checkpoint").Wrap(err)
	}
	buf = append(buf, '\n')
	if _, err := f.Write(buf); err != nil {
		return r, model.NewError(model.CodeCheckpointCorrupt, model.Loc{File: s.path()}, "write checkpoint").Wrap(err)
	}
	if err := f.Sync(); err != nil {
		return r, model.NewError(model.CodeCheckpointCorrupt, model.Loc{File: s.path()}, "sync checkpoint").Wrap(err)
	}
	return r, nil
}

// LoadResult is the outcome of reading a checkpoint log.
type LoadResult struct {
	// Valid is the newest checkpoint whose digest verifies.
	Valid *Record
	// All lists every record whose own digest verifies, oldest first. Callers
	// that can verify payloads must walk it backwards to fall back.
	All []Record
	// Records is the number of complete records parsed.
	Records int
	// Corrupt is the number of records that failed verification.
	Corrupt int
	// FallbackReason is non-empty when the newest record was rejected.
	FallbackReason string
	// DamagedOffset is the byte offset of the first damaged record.
	DamagedOffset int64
}

// Load reads the log and returns every record whose own digest verifies. A
// damaged record stops the scan: nothing after a damaged record is trusted,
// because checkpoints form a chain.
func (s *Store) Load() (LoadResult, error) {
	var res LoadResult
	f, err := os.Open(s.path())
	if err != nil {
		if os.IsNotExist(err) {
			return res, model.NewError(model.CodeCheckpointMissing, model.Loc{File: s.path()}, "no checkpoint log")
		}
		return res, model.NewError(model.CodeCheckpointCorrupt, model.Loc{File: s.path()}, "open checkpoint log").Wrap(err)
	}
	defer f.Close()

	br := bufio.NewReader(f)
	var off int64
	for {
		line, rerr := br.ReadBytes('\n')
		if len(line) > 0 {
			if rerr == io.EOF && !bytes.HasSuffix(line, []byte("\n")) {
				res.Corrupt++
				res.DamagedOffset = off
				res.FallbackReason = fmt.Sprintf("truncated record at byte %d", off)
				break
			}
			var r Record
			dec := json.NewDecoder(bytes.NewReader(line))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&r); err != nil {
				res.Corrupt++
				res.DamagedOffset = off
				res.FallbackReason = fmt.Sprintf("undecodable record at byte %d: %v", off, err)
				break
			}
			res.Records++
			if r.Digest != r.digest() {
				res.Corrupt++
				res.DamagedOffset = off
				res.FallbackReason = fmt.Sprintf("digest mismatch at byte %d", off)
				break
			}
			res.All = append(res.All, r)
		}
		off += int64(len(line))
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return res, model.NewError(model.CodeCheckpointCorrupt, model.Loc{File: s.path()}, "read checkpoint log").Wrap(rerr)
		}
	}
	if len(res.All) == 0 {
		return res, model.NewError(model.CodeCheckpointCorrupt, model.Loc{File: s.path()}, "no valid checkpoint record")
	}
	v := res.All[len(res.All)-1]
	res.Valid = &v
	return res, nil
}

// Candidates returns the verified records newest first, for payload-level
// fallback.
func (r LoadResult) Candidates() []Record {
	out := make([]Record, len(r.All))
	for i := range r.All {
		out[i] = r.All[len(r.All)-1-i]
	}
	return out
}

// Compact rewrites the log keeping only the newest MaxRecords valid entries.
func (s *Store) Compact() error {
	res, err := s.Load()
	if err != nil {
		return err
	}
	if res.Valid == nil {
		return nil
	}
	all, err := s.readAllValid()
	if err != nil {
		return err
	}
	if len(all) <= s.MaxRecords {
		return nil
	}
	all = all[len(all)-s.MaxRecords:]
	tmp := s.path() + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, r := range all {
		buf, err := json.Marshal(r)
		if err != nil {
			f.Close()
			return err
		}
		w.Write(buf)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.path())
}

func (s *Store) readAllValid() ([]Record, error) {
	f, err := os.Open(s.path())
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	for sc.Scan() {
		var r Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		if r.Digest == r.digest() {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out, sc.Err()
}

// Remove deletes the checkpoint log, used when a run starts fresh.
func (s *Store) Remove() error {
	err := os.Remove(s.path())
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
