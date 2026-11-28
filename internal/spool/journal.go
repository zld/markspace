// Package spool keeps a durable journal of which byte ranges of the
// reassembled file have been written to the local segment store, together
// with their checksums. The journal is the checkpoint used to resume after
// an interrupted run: replayed ranges must match what a complete run would
// have produced.
package spool

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"mediakernel/internal/fault"
)

// Entry describes one committed byte range and its content checksum.
type Entry struct {
	Offset uint64 `json:"offset"`
	Length uint64 `json:"length"`
	SHA256 string `json:"sha256"`
}

// Journal is an append-only log of committed ranges backed by a JSON-lines
// file.
type Journal struct {
	path    string
	entries []Entry
	file    *os.File
}

// Open creates (or appends to) the journal at path.
func Open(path string) (*Journal, error) {
	j := &Journal{path: path}
	if _, err := os.Stat(path); err == nil {
		if err := j.load(); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	j.file = f
	return j, nil
}

func (j *Journal) load() error {
	f, err := os.Open(j.path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return fault.New(fault.ErrCorruptBox, int64(line),
				"journal line %d unreadable: %v", line, err)
		}
		j.entries = append(j.entries, e)
	}
	return sc.Err()
}

// Append records one committed range and flushes it to disk so a later
// resume can observe it.
func (j *Journal) Append(e Entry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := j.file.Write(append(b, '\n')); err != nil {
		return err
	}
	if err := j.file.Sync(); err != nil {
		return err
	}
	j.entries = append(j.entries, e)
	return nil
}

// Entries returns the recorded ranges sorted by offset.
func (j *Journal) Entries() []Entry {
	out := make([]Entry, len(j.entries))
	copy(out, j.entries)
	sort.Slice(out, func(i, k int) bool { return out[i].Offset < out[k].Offset })
	return out
}

// Covers reports whether the whole range [offset, offset+length) is present
// in the committed entries.
func (j *Journal) Covers(offset, length uint64) bool {
	end := offset + length
	cursor := offset
	for _, e := range j.Entries() {
		if e.Offset > cursor {
			return false // gap before the next committed range
		}
		if e.Offset+e.Length > cursor {
			cursor = e.Offset + e.Length
		}
		if cursor >= end {
			return true
		}
	}
	return false
}

// Close flushes and closes the journal file.
func (j *Journal) Close() error {
	if j.file == nil {
		return nil
	}
	return j.file.Close()
}

// String summarizes the journal for terminal output.
func (j *Journal) String() string {
	return fmt.Sprintf("%d committed ranges", len(j.entries))
}
