// Package checkpoint persists batch progress as an append-only JSONL log
// with atomic checkpoint commits, so an interrupted run resumes from the last
// durable block instead of restarting.
//
// The log is authoritative: each record is framed by length and CRC and
// appended with a single write. A checkpoint file is written to a temporary
// name, fsynced, and renamed into place, so a crash can leave at most a torn
// tail record, which replay detects and discards.
package checkpoint

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/pairwise/mediad/internal/fault"
)

// Record is one committed chunk result.
type Record struct {
	Block       int    `json:"block"`
	TrackID     uint32 `json:"track_id"`
	StartNS     int64  `json:"start_ns"`
	EndNS       int64  `json:"end_ns"`
	FirstSample int    `json:"first_sample"`
	Samples     int    `json:"samples"`
	Content     string `json:"content"`
	SilentNS    int64  `json:"silent_ns"`
	KeyExact    bool   `json:"key_exact"`
	Recovered   bool   `json:"recovered"`
}

// State is the replayed, verified progress of a batch.
type State struct {
	Records   []Record
	Blocks    map[int][]Record
	Index     map[identity]Record
	TornTail  bool
	NextBlock int
}

// Store appends records and commits checkpoints under a directory.
type Store struct {
	dir      string
	logPath  string
	ckptPath string
	mu       sync.Mutex
	log      *os.File
	state    State
}

// identity keys a committed segment independently of block grouping, so a
// resumed run can skip work even if its block budget differs.
type identity struct {
	trackID uint32
	startNS int64
	endNS   int64
	first   int
}

// Open loads existing progress (if any) and opens the append log.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fault.New("checkpoint.open", fault.KindCheckpoint, err).WithPath(dir)
	}
	s := &Store{dir: dir, logPath: filepath.Join(dir, "journal.jsonl"), ckptPath: filepath.Join(dir, "checkpoint.json")}
	st, err := s.load()
	if err != nil {
		return nil, err
	}
	s.state = st
	f, err := os.OpenFile(s.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fault.New("checkpoint.open", fault.KindCheckpoint, err).WithPath(s.logPath)
	}
	s.log = f
	return s, nil
}

// Close releases the log.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.log != nil {
		err := s.log.Close()
		s.log = nil
		return err
	}
	return nil
}

func identLess(a, b Record) bool {
	if a.TrackID != b.TrackID {
		return a.TrackID < b.TrackID
	}
	if a.StartNS != b.StartNS {
		return a.StartNS < b.StartNS
	}
	if a.EndNS != b.EndNS {
		return a.EndNS < b.EndNS
	}
	return a.FirstSample < b.FirstSample
}

func identOf(r Record) identity {
	return identity{r.TrackID, r.StartNS, r.EndNS, r.FirstSample}
}

func (s *Store) load() (State, error) {
	st := State{Blocks: map[int][]Record{}, Index: map[identity]Record{}}
	var ckpt struct {
		NextBlock int      `json:"next_block"`
		Records   []Record `json:"records"`
	}
	if b, err := os.ReadFile(s.ckptPath); err == nil {
		if err := json.Unmarshal(b, &ckpt); err != nil {
			return st, fault.New("checkpoint.load", fault.KindCheckpoint, err).WithPath(s.ckptPath).Fatal()
		}
	}
	recs, torn, valid, err := readJournal(s.logPath)
	if err != nil {
		return st, err
	}
	if torn {
		// Drop the unparseable suffix before appending; otherwise every later
		// record would sit behind the torn one and be invisible to replay.
		if err := os.Truncate(s.logPath, valid); err != nil {
			return st, fault.New("checkpoint.repair", fault.KindCheckpoint, err).WithPath(s.logPath)
		}
	}
	// The checkpoint is a subset of the journal; replay merges both, keyed by
	// block, and the journal wins because it is appended first.
	merged := map[int]map[string]Record{}
	add := func(r Record) {
		if merged[r.Block] == nil {
			merged[r.Block] = map[string]Record{}
		}
		merged[r.Block][fmt.Sprintf("%d|%d|%d|%d", r.TrackID, r.StartNS, r.EndNS, r.FirstSample)] = r
	}
	for _, r := range ckpt.Records {
		add(r)
	}
	for _, r := range recs {
		add(r)
	}
	blocks := make([]int, 0, len(merged))
	for b := range merged {
		blocks = append(blocks, b)
	}
	sort.Ints(blocks)
	for _, b := range blocks {
		// Map iteration order is random; sort within a block so replay is
		// byte-for-byte reproducible across opens.
		batch := make([]Record, 0, len(merged[b]))
		for _, r := range merged[b] {
			batch = append(batch, r)
		}
		sort.Slice(batch, func(i, j int) bool { return identLess(batch[i], batch[j]) })
		for _, r := range batch {
			st.Records = append(st.Records, r)
			st.Blocks[b] = append(st.Blocks[b], r)
			st.Index[identOf(r)] = r
		}
	}
	st.TornTail = torn
	if n := len(blocks); n > 0 {
		st.NextBlock = blocks[n-1] + 1
	}
	if ckpt.NextBlock > st.NextBlock {
		st.NextBlock = ckpt.NextBlock
	}
	return st, nil
}

// readJournal parses length+CRC framed JSON records and reports a torn tail.
func readJournal(path string) ([]Record, bool, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, 0, nil
		}
		return nil, false, 0, fault.New("checkpoint.read", fault.KindCheckpoint, err).WithPath(path)
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 32*1024)
	var out []Record
	var valid int64
	for {
		var hdr [8]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if err == io.EOF {
				return out, false, valid, nil
			}
			return out, true, valid, nil // partial header = torn tail
		}
		n := binary.BigEndian.Uint32(hdr[0:4])
		want := binary.BigEndian.Uint32(hdr[4:8])
		if n == 0 || n > 1<<20 {
			return out, true, valid, nil
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(r, body); err != nil {
			return out, true, valid, nil
		}
		if crc32.ChecksumIEEE(body) != want {
			return out, true, valid, nil
		}
		var rec Record
		if err := json.Unmarshal(body, &rec); err != nil {
			return out, true, valid, nil
		}
		valid += int64(8 + n)
		out = append(out, rec)
	}
}

// Recovered reports whether the previous run left a torn record.
func (s *Store) Recovered() bool { return s.state.TornTail }

// NextBlock reports the first block not yet visible to replay.
func (s *Store) NextBlock() int { return s.state.NextBlock }

// Completed reports whether operations for block were already committed.
func (s *Store) Completed(block int) bool { return len(s.state.Blocks[block]) > 0 }

// Has reports whether a record with this identity was already committed.
func (s *Store) Has(trackID uint32, startNS, endNS int64, firstSample int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.state.Index[identity{trackID, startNS, endNS, firstSample}]
	return ok
}

// BlockRecords returns a copy of the committed records for one block.
func (s *Store) BlockRecords(block int) []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Record(nil), s.state.Blocks[block]...)
}

// Lookup returns committed records matching a segment identity.
func (s *Store) Lookup(trackID uint32, startNS, endNS int64, firstSample int) []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.state.Index[identity{trackID, startNS, endNS, firstSample}]
	if !ok {
		return nil
	}
	return []Record{r}
}

// Records returns a copy of committed records in block order.
func (s *Store) Records() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Record(nil), s.state.Records...)
}

// Append logs one record durably and returns once it is on disk.
func (s *Store) Append(recs ...Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.log == nil {
		return fault.Errorf("checkpoint.append", fault.KindCheckpoint, "store closed")
	}
	for _, r := range recs {
		body, err := json.Marshal(r)
		if err != nil {
			return fault.New("checkpoint.append", fault.KindCheckpoint, err)
		}
		var hdr [8]byte
		binary.BigEndian.PutUint32(hdr[0:4], uint32(len(body)))
		binary.BigEndian.PutUint32(hdr[4:8], crc32.ChecksumIEEE(body))
		if _, err := s.log.Write(hdr[:]); err != nil {
			return fault.New("checkpoint.append", fault.KindCheckpoint, err).WithPath(s.logPath)
		}
		if _, err := s.log.Write(body); err != nil {
			return fault.New("checkpoint.append", fault.KindCheckpoint, err).WithPath(s.logPath)
		}
	}
	if err := s.log.Sync(); err != nil {
		return fault.New("checkpoint.sync", fault.KindCheckpoint, err).WithPath(s.logPath)
	}
	for _, r := range recs {
		s.state.Blocks[r.Block] = append(s.state.Blocks[r.Block], r)
		s.state.Records = append(s.state.Records, r)
		s.state.Index[identOf(r)] = r
		if r.Block+1 > s.state.NextBlock {
			s.state.NextBlock = r.Block + 1
		}
	}
	return nil
}

// Commit atomically publishes a checkpoint covering every committed block.
func (s *Store) Commit() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := struct {
		NextBlock int      `json:"next_block"`
		Records   []Record `json:"records"`
	}{NextBlock: s.state.NextBlock, Records: s.state.Records}
	body, err := json.Marshal(state)
	if err != nil {
		return fault.New("checkpoint.commit", fault.KindCheckpoint, err)
	}
	tmp := s.ckptPath + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fault.New("checkpoint.commit", fault.KindCheckpoint, err).WithPath(tmp)
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		return fault.New("checkpoint.commit", fault.KindCheckpoint, err).WithPath(tmp)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fault.New("checkpoint.commit", fault.KindCheckpoint, err).WithPath(tmp)
	}
	if err := f.Close(); err != nil {
		return fault.New("checkpoint.commit", fault.KindCheckpoint, err).WithPath(tmp)
	}
	if err := os.Rename(tmp, s.ckptPath); err != nil {
		return fault.New("checkpoint.commit", fault.KindCheckpoint, err).WithPath(s.ckptPath)
	}
	if d, err := os.Open(s.dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}
