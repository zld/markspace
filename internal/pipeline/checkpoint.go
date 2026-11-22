// Package pipeline orders the transport, container, sync and index stages and
// makes every completed stage checkpointed and cache-verified. A simulated
// process interruption resumes only from the last complete commit point.
package pipeline

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"mediapipe/internal/cache"
)

// Checkpoint limits.
const (
	MaxRecordBytes = 256 << 10
	MaxRecords     = 4096
	MaxCommitted   = 4096
)

var (
	// ErrCheckpointFormat reports an unreadable checkpoint journal line.
	ErrCheckpointFormat = errors.New("pipeline: malformed checkpoint")
	// ErrCheckpointDigest reports a record whose state digest does not match.
	ErrCheckpointDigest = errors.New("pipeline: checkpoint digest mismatch")
	// ErrCheckpointRegression reports a generation that moves backwards.
	ErrCheckpointRegression = errors.New("pipeline: checkpoint generation regression")
	// ErrResumeMismatch reports a resume whose input no longer matches.
	ErrResumeMismatch = errors.New("pipeline: resume input mismatch")
)

// CommittedFragment is one fragment proven complete at checkpoint time.
type CommittedFragment struct {
	Sequence uint32    `json:"sequence"`
	Digest   cache.Key `json:"digest"`
	Tracks   []uint32  `json:"tracks"`
}

// SeqWindow is the RTP sequence window covered by the committed work.
type SeqWindow struct {
	Start uint16 `json:"start"`
	End   uint16 `json:"end"`
	Count int    `json:"count"`
}

// Record is one complete commit point.
type Record struct {
	Format      string              `json:"format"`
	Generation  uint64              `json:"generation"`
	Stage       string              `json:"stage"`
	Version     string              `json:"version"`
	Input       cache.Key           `json:"input"`
	Output      cache.Key           `json:"output"`
	SeqWindow   SeqWindow           `json:"seq_window"`
	Committed   []CommittedFragment `json:"committed"`
	CompletedAt int64               `json:"completed_at"`
	StateDigest string              `json:"state_digest"`
}

const recordFormat = "mediapipe-checkpoint/1"

func (r *Record) computeDigest() string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%d|%s|%s|%s|%s|%d|%d|%d|", r.Format, r.Generation, r.Stage, r.Version,
		r.Input, r.Output, r.SeqWindow.Start, r.SeqWindow.End, r.SeqWindow.Count)
	for _, c := range r.Committed {
		fmt.Fprintf(h, "%d:%s:%v;", c.Sequence, c.Digest, c.Tracks)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Validate verifies all fields of a record.
func (r *Record) Validate() error {
	if r.Format != recordFormat {
		return fmt.Errorf("%w: format %q", ErrCheckpointFormat, r.Format)
	}
	if r.Stage == "" || len(r.Stage) > 64 || r.Version == "" || len(r.Version) > 64 {
		return fmt.Errorf("%w: stage/version", ErrCheckpointFormat)
	}
	if !r.Input.Valid() || !r.Output.Valid() {
		return fmt.Errorf("%w: digests", ErrCheckpointFormat)
	}
	if r.SeqWindow.Count < 0 || r.SeqWindow.Count > 65536 {
		return fmt.Errorf("%w: window count %d", ErrCheckpointFormat, r.SeqWindow.Count)
	}
	if len(r.Committed) > MaxCommitted {
		return fmt.Errorf("%w: %d committed fragments", ErrCheckpointFormat, len(r.Committed))
	}
	for i, c := range r.Committed {
		if c.Sequence == 0 || !c.Digest.Valid() || len(c.Tracks) == 0 {
			return fmt.Errorf("%w: committed[%d]", ErrCheckpointFormat, i)
		}
		if i > 0 && r.Committed[i-1].Sequence >= c.Sequence {
			return fmt.Errorf("%w: committed out of order at %d", ErrCheckpointFormat, i)
		}
	}
	if r.StateDigest != r.computeDigest() {
		return fmt.Errorf("%w: stage %s generation %d", ErrCheckpointDigest, r.Stage, r.Generation)
	}
	return nil
}

// Reset removes the journal, starting a fresh lineage of commit points. It is
// used when a run is not a resume: the checkpoint directory describes one
// pipeline execution history, not an accumulation of unrelated runs.
func (j *Journal) Reset() error {
	if err := os.Remove(j.path()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Journal is an append-only checkpoint log. Each line is length-prefixed and
// self-checksummed, so a torn write at process death is detected and ignored.
type Journal struct {
	dir string
}

// OpenJournal creates the checkpoint directory.
func OpenJournal(dir string) (*Journal, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}
	return &Journal{dir: abs}, nil
}

func (j *Journal) path() string { return filepath.Join(j.dir, "checkpoint.log") }

// Append commits one record. Data is appended and fsynced before return; a
// partially written final line is harmless to Load.
func (j *Journal) Append(r Record) error {
	if r.Format == "" {
		r.Format = recordFormat
	}
	r.StateDigest = r.computeDigest()
	if err := r.Validate(); err != nil {
		return err
	}
	enc, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(enc) > MaxRecordBytes {
		return fmt.Errorf("%w: record %d bytes", ErrCheckpointFormat, len(enc))
	}
	sum := sha256.Sum256(enc)
	line := fmt.Sprintf("%d %s %s\n", len(enc), hex.EncodeToString(sum[:8]), enc)
	f, err := os.OpenFile(j.path(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.WriteString(f, line); err != nil {
		return err
	}
	return f.Sync()
}

// Load returns the last complete, valid record. Trailing corruption and torn
// records are ignored. Generation must strictly increase.
func (j *Journal) Load() (*Record, int, error) {
	f, err := os.Open(j.path())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), MaxRecordBytes+64)
	var last *Record
	good := 0
	var prevGen uint64
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		r, ok := decodeLine(line)
		if !ok {
			continue // torn or corrupt line: stop trusting, but keep prior state
		}
		if err := r.Validate(); err != nil {
			continue
		}
		if r.Generation <= prevGen {
			return last, good, fmt.Errorf("%w: %d after %d", ErrCheckpointRegression, r.Generation, prevGen)
		}
		prevGen = r.Generation
		last = r
		good++
		if good > MaxRecords {
			return nil, 0, fmt.Errorf("%w: too many records", ErrCheckpointFormat)
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, bufio.ErrTooLong) {
		return last, good, nil
	}
	return last, good, nil
}

func decodeLine(line []byte) (*Record, bool) {
	var n int
	var hexSum string
	sp1 := bytes.IndexByte(line, ' ')
	if sp1 <= 0 {
		return nil, false
	}
	if _, err := fmt.Sscanf(string(line[:sp1]), "%d", &n); err != nil {
		return nil, false
	}
	rest := line[sp1+1:]
	sp2 := bytes.IndexByte(rest, ' ')
	if sp2 <= 0 {
		return nil, false
	}
	hexSum = string(rest[:sp2])
	payload := rest[sp2+1:]
	if n != len(payload) {
		return nil, false
	}
	sum := sha256.Sum256(payload)
	if hex.EncodeToString(sum[:8]) != hexSum {
		return nil, false
	}
	var r Record
	if err := json.Unmarshal(payload, &r); err != nil {
		return nil, false
	}
	return &r, true
}

// Truncate removes every record after the last valid one, used after a crash to
// make the journal writable from a clean state.
func (j *Journal) Truncate(keep int) error {
	f, err := os.Open(j.path())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), MaxRecordBytes+64)
	var out bytes.Buffer
	count := 0
	for sc.Scan() && count < keep {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		if _, ok := decodeLine(line); !ok {
			break
		}
		out.Write(line)
		out.WriteByte('\n')
		count++
	}
	f.Close()
	if err := os.WriteFile(j.path()+".tmp", out.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(j.path()+".tmp", j.path())
}

// Records reads all valid records in order, for diagnostics and tests.
func (j *Journal) Records() ([]Record, error) {
	f, err := os.Open(j.path())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), MaxRecordBytes+64)
	var out []Record
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		r, ok := decodeLine(line)
		if !ok {
			break
		}
		if err := r.Validate(); err != nil {
			break
		}
		out = append(out, *r)
	}
	return out, nil
}

// String summarizes a record for terminal output.
func (r Record) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "gen=%d stage=%s in=%s out=%s seq=[%d..%d]x%d fragments=%d",
		r.Generation, r.Stage, r.Input.String()[:12], r.Output.String()[:12],
		r.SeqWindow.Start, r.SeqWindow.End, r.SeqWindow.Count, len(r.Committed))
	return b.String()
}
