package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// appendLocked appends one journal line then atomically replaces checkpoint.
func (s *Store) appendLocked(ev Event) error {
	ev.Timestamp = time.Now().UTC()
	line, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("store: marshal event: %w", err)
	}
	if _, err := s.writer.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("store: journal write: %w", err)
	}
	if err := s.writer.Flush(); err != nil {
		return fmt.Errorf("store: journal flush: %w", err)
	}
	if err := s.journal.Sync(); err != nil {
		return fmt.Errorf("store: journal sync: %w", err)
	}
	applyEvent(s.cp, &ev)
	s.cp.UpdatedAt = ev.Timestamp
	if err := s.persistLocked(); err != nil {
		return err
	}
	return nil
}

// persistLocked writes checkpoint.json via temp file + rename + fsync.
func (s *Store) persistLocked() error {
	cpPath := filepath.Join(s.dir, cpName)
	tmp, err := os.CreateTemp(s.dir, ".checkpoint-*.tmp")
	if err != nil {
		return fmt.Errorf("store: checkpoint temp: %w", err)
	}
	tmpName := tmp.Name()
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s.cp); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("store: checkpoint encode: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("store: checkpoint sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("store: checkpoint close: %w", err)
	}
	if err := os.Rename(tmpName, cpPath); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("store: checkpoint rename: %w", err)
	}
	return nil
}

// Close flushes and closes the journal.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writer.Flush(); err != nil {
		return err
	}
	return s.journal.Close()
}
