package checkpoint

import (
	"os"
	"path/filepath"
	"testing"

	"mediatimeline/internal/clock"
)

func TestSaveAndReload(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Latest() != nil {
		t.Fatal("fresh store should have no checkpoint")
	}
	for seq := uint64(1); seq <= 4; seq++ {
		if err := s.Save(Checkpoint{Sequence: seq, Payload: []byte{byte(seq), 2, 3}}); err != nil {
			t.Fatal(err)
		}
	}
	// Reopen: newest valid slot wins regardless of A/B ordering.
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := s2.Latest()
	if got == nil || got.Sequence != 4 || len(got.Payload) != 3 || got.Payload[0] != 4 {
		t.Fatalf("latest=%+v", got)
	}
	// Both slot files exist and no temp files are left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == tmpSuffix {
			t.Fatalf("temp file leaked: %s", e.Name())
		}
	}
}

func TestCorruptSlotFallsBack(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	// Three saves leave the newest in one slot and an older one in the other.
	_ = s.Save(Checkpoint{Sequence: 1, Payload: []byte("one")})
	_ = s.Save(Checkpoint{Sequence: 2, Payload: []byte("two")})
	_ = s.Save(Checkpoint{Sequence: 3, Payload: []byte("three")})

	// Corrupt whichever slot holds sequence 3 by flipping its CRC-protected byte.
	for _, name := range []string{slotA, slotB} {
		p := filepath.Join(dir, name)
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		seq := uint64(raw[8])<<56 | uint64(raw[9])<<48 | uint64(raw[10])<<40 |
			uint64(raw[11])<<32 | uint64(raw[12])<<24 | uint64(raw[13])<<16 |
			uint64(raw[14])<<8 | uint64(raw[15])
		if seq == 3 {
			raw[headerBytes] ^= 0xFF
			if err := os.WriteFile(p, raw, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("fallback failed: %v", err)
	}
	if s2.Latest() == nil || s2.Latest().Sequence != 2 {
		t.Fatalf("expected fallback to seq 2, got %+v", s2.Latest())
	}
}

func TestBothSlotsCorruptIsUnrecoverable(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	_ = s.Save(Checkpoint{Sequence: 1, Payload: []byte("x")})
	_ = s.Save(Checkpoint{Sequence: 2, Payload: []byte("yy")})
	if err := s.Corrupt(); err != nil {
		t.Fatal(err)
	}
	_, err := Open(dir)
	if err == nil {
		t.Fatal("expected corruption error")
	}
	if c, ok := err.(*clock.Error); !ok || c.Code != clock.ErrCheckpointCorrupt {
		t.Fatalf("want CHECKPOINT_CORRUPT got %v", err)
	}
}

func TestSaveRejectsZeroSequence(t *testing.T) {
	s, _ := Open(t.TempDir())
	if err := s.Save(Checkpoint{Sequence: 0, Payload: []byte("z")}); err == nil {
		t.Fatal("zero sequence accepted")
	}
}
