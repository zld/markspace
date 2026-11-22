// Package checkpoint provides atomic, integrity-checked persistence of
// pipeline state. Writes go to a temporary file and are renamed into place;
// two alternating slots guarantee that a torn or corrupted write falls back
// to the most recent valid checkpoint rather than losing all progress.
package checkpoint

import (
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"

	"mediatimeline/internal/clock"
)

const (
	magic       = "MTLCKP01"
	slotA       = "checkpoint.a.bin"
	slotB       = "checkpoint.b.bin"
	tmpSuffix   = ".tmp"
	headerBytes = 8 + 8 + 4 // magic + sequence + payload crc = 20
)

// Checkpoint is the versioned state blob exchanged with the pipeline. The
// payload is opaque JSON produced by the pipeline; this package owns framing
// and integrity only.
type Checkpoint struct {
	Sequence uint64
	Payload  []byte
}

// Store manages two on-disk slots inside a directory.
type Store struct {
	dir      string
	useSlotA bool
	last     *Checkpoint
	crcTable *crc32.Table
}

// Open creates the checkpoint directory if needed and loads the newest valid
// slot. A fully corrupted store returns ErrCheckpointCorrupt only when both
// slots exist but neither validates; an absent store starts empty.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, useSlotA: true, crcTable: crc32.MakeTable(crc32.IEEE)}
	a, errA := s.readSlot(slotA)
	b, errB := s.readSlot(slotB)
	noA := a == nil && os.IsNotExist(errA)
	noB := b == nil && os.IsNotExist(errB)
	switch {
	case errA == nil && errB == nil:
		if b.Sequence > a.Sequence {
			s.last, s.useSlotA = b, false
		} else {
			s.last, s.useSlotA = a, true
		}
	case errA == nil:
		s.last, s.useSlotA = a, true
	case errB == nil:
		s.last, s.useSlotA = b, false
	case noA && noB:
		// Fresh workspace: nothing to restore yet.
	default:
		return nil, clock.NewError(clock.ErrCheckpointCorrupt,
			"both checkpoint slots unreadable").WithPosition(-1, 0, -1)
	}
	return s, nil
}

// readSlot validates magic and CRC before returning the payload.
func (s *Store) readSlot(name string) (*Checkpoint, error) {
	raw, err := os.ReadFile(filepath.Join(s.dir, name))
	if err != nil {
		return nil, err
	}
	if len(raw) < headerBytes || string(raw[:8]) != magic {
		return nil, clock.NewError(clock.ErrCheckpointCorrupt, "bad magic in "+name)
	}
	seq := binary.BigEndian.Uint64(raw[8:16])
	wantCRC := binary.BigEndian.Uint32(raw[16:20])
	payload := raw[headerBytes:]
	if crc32.Checksum(payload, s.crcTable) != wantCRC {
		return nil, clock.NewError(clock.ErrCheckpointCorrupt, "crc mismatch in "+name)
	}
	return &Checkpoint{Sequence: seq, Payload: append([]byte(nil), payload...)}, nil
}

// Save frames and atomically replaces the inactive slot, then flips slots so
// the previous valid checkpoint remains available for fallback.
func (s *Store) Save(cp Checkpoint) error {
	if cp.Sequence == 0 {
		return clock.NewError(clock.ErrInvariant, "checkpoint sequence must be positive")
	}
	frame := make([]byte, headerBytes+len(cp.Payload))
	copy(frame[:8], magic)
	binary.BigEndian.PutUint64(frame[8:16], cp.Sequence)
	crc := crc32.Checksum(cp.Payload, s.crcTable)
	binary.BigEndian.PutUint32(frame[16:20], crc)
	copy(frame[headerBytes:], cp.Payload)

	// Write the inactive slot so the currently valid checkpoint remains on
	// disk untouched until the atomic rename lands; then flip to make the new
	// slot active, leaving the previous one as the fallback.
	target := slotA
	if s.useSlotA {
		target = slotB
	}
	final := filepath.Join(s.dir, target)
	tmp := final + tmpSuffix
	if err := os.WriteFile(tmp, frame, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		return err
	}
	s.last = &Checkpoint{Sequence: cp.Sequence, Payload: append([]byte(nil), cp.Payload...)}
	s.useSlotA = !s.useSlotA
	return nil
}

// Latest returns the most recently validated checkpoint, or nil if none.
func (s *Store) Latest() *Checkpoint { return s.last }

// Corrupt marks both on-disk slots as damaged without touching in-memory
// state; it backs the `corrupt --kind checkpoint` demonstration path.
func (s *Store) Corrupt() error {
	for _, name := range []string{slotA, slotB} {
		p := filepath.Join(s.dir, name)
		raw, err := os.ReadFile(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if len(raw) > headerBytes {
			raw[headerBytes] ^= 0xFF // flip a payload byte, invalidating CRC
		}
		if err := os.WriteFile(p+tmpSuffix, raw, 0o600); err != nil {
			return err
		}
		if err := os.Rename(p+tmpSuffix, p); err != nil {
			return err
		}
	}
	return nil
}
