package fixture

import (
	"os"
	"path/filepath"
	"testing"

	"mediaforge/internal/mediaerr"
	"mediaforge/internal/packet"
)

func readCapture(t *testing.T, dir, name string) *packet.Stream {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s, err := packet.ReadStream(f, packet.MaxInputSize)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return s
}

func TestGenerateAllFixtures(t *testing.T) {
	dir := t.TempDir()
	if err := Generate(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range All() {
		if st, err := os.Stat(filepath.Join(dir, name)); err != nil || st.Size() == 0 {
			t.Fatalf("%s missing: %v", name, err)
		}
	}
}

func TestNormalHasNoErrors(t *testing.T) {
	dir := t.TempDir()
	if err := Generate(dir); err != nil {
		t.Fatal(err)
	}
	s := readCapture(t, dir, Normal)
	bad := 0
	for _, r := range s.Records {
		if r.Err != nil {
			bad++
		}
	}
	if bad != 0 || len(s.Records) != TotalFrames {
		t.Fatalf("records=%d bad=%d", len(s.Records), bad)
	}
}

func TestReorderedIsDeduplicable(t *testing.T) {
	dir := t.TempDir()
	if err := Generate(dir); err != nil {
		t.Fatal(err)
	}
	s := readCapture(t, dir, Reordered)
	if len(s.Records) <= TotalFrames {
		t.Fatalf("expected duplicates, records=%d", len(s.Records))
	}
	seqs := map[uint16]bool{}
	for _, r := range s.Records {
		if r.Packet != nil {
			seqs[r.Packet.Seq] = true
		}
	}
	if len(seqs) != TotalFrames {
		t.Fatalf("unique=%d", len(seqs))
	}
}

func TestDamagedContainsEveryFault(t *testing.T) {
	dir := t.TempDir()
	if err := Generate(dir); err != nil {
		t.Fatal(err)
	}
	s := readCapture(t, dir, Damaged)
	var badVersion, corrupt, truncated, missing int
	seen := map[uint16]bool{}
	for _, r := range s.Records {
		if r.Err != nil {
			switch mediaerr.CodeOf(r.Err) {
			case mediaerr.CodeTruncated:
				truncated++
			case mediaerr.CodeCorrupt:
				corrupt++
			case mediaerr.CodeBadPacket:
				badVersion++
			}
		}
		if r.Packet != nil && r.Err == nil {
			seen[r.Packet.Seq] = true
		}
	}
	if badVersion != 1 || corrupt != 1 || truncated != 1 {
		t.Fatalf("badVersion=%d corrupt=%d truncated=%d", badVersion, corrupt, truncated)
	}
	for i := 90; i < 96; i++ {
		if seen[uint16(i)] {
			missing++
		}
	}
	if missing != 0 {
		t.Fatalf("dropped packets still present: %d", missing)
	}
}

func TestWrapHasLateArrivals(t *testing.T) {
	dir := t.TempDir()
	if err := Generate(dir); err != nil {
		t.Fatal(err)
	}
	s := readCapture(t, dir, Wrap)
	if len(s.Records) != 120 {
		t.Fatalf("records=%d", len(s.Records))
	}
	// The three held packets appear after packet 30 was delivered.
	pos := map[uint16]int{}
	for idx, r := range s.Records {
		if r.Packet != nil {
			pos[r.Packet.Seq] = idx
		}
	}
	for _, late := range []uint16{15, 16, 22} {
		if pos[late] <= pos[30] {
			t.Fatalf("late packet %d delivered at %d before packet 30 at %d",
				late, pos[late], pos[30])
		}
	}
}
