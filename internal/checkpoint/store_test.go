package checkpoint

import (
	"os"
	"path/filepath"
	"testing"

	"mediapack/internal/fault"
)

func writeInput(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func openFor(t *testing.T, dir, input string, batch int) (*Store, [32]byte) {
	t.Helper()
	sum, _, err := FileSHA256(input)
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(filepath.Join(dir, "index"), batch, input, sum)
	if err != nil {
		t.Fatal(err)
	}
	return s, sum
}

func TestFreshLoadReturnsNoState(t *testing.T) {
	dir := t.TempDir()
	input := writeInput(t, dir, "in.webm", "hello")
	s, _ := openFor(t, dir, input, 2)
	st, err := s.Load()
	if err != nil || st != nil {
		t.Fatalf("fresh index must be empty, got st=%v err=%v", st, err)
	}
}

func TestCommitAndReload(t *testing.T) {
	dir := t.TempDir()
	input := writeInput(t, dir, "in.webm", "hello world")
	s, _ := openFor(t, dir, input, 2)
	h1 := [32]byte{1}
	h2 := [32]byte{2}
	st, err := s.Commit(11, 2, [][32]byte{h1, h2})
	if err != nil {
		t.Fatal(err)
	}
	if st.BlockCount != 2 || st.CommittedBytes != 11 || st.BatchSize != 2 {
		t.Fatalf("committed state wrong: %s", st)
	}
	loaded, err := s.Load()
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if loaded.BlockCount != 2 || loaded.CommittedBytes != 11 {
		t.Fatalf("reloaded state wrong: %s", loaded)
	}
}

func TestCorruptIndexRejectedWithOffset(t *testing.T) {
	dir := t.TempDir()
	input := writeInput(t, dir, "in.webm", "hello")
	s, _ := openFor(t, dir, input, 2)
	if _, err := s.Commit(5, 1, [][32]byte{{9}}); err != nil {
		t.Fatal(err)
	}
	// Flip a byte inside the stored counters to break the self-checksum.
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-40] ^= 0x01
	if err := os.WriteFile(s.Path(), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = s.Load()
	fe := fault.Of(err)
	if fe == nil || fe.Kind != fault.KindCheckpointCorrupt || !fe.Recoverable {
		t.Fatalf("expected recoverable corrupt-index fault, got %v", err)
	}
	if fe.Offset < 0 {
		t.Fatal("corrupt-index fault must name a checkpoint offset")
	}
}

func TestMalformedJSONRejected(t *testing.T) {
	dir := t.TempDir()
	input := writeInput(t, dir, "in.webm", "hello")
	s, _ := openFor(t, dir, input, 2)
	if err := os.WriteFile(s.Path(), []byte("{not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := s.Load()
	if !fault.Is(err, fault.KindCheckpointCorrupt) {
		t.Fatalf("expected corrupt-index fault, got %v", err)
	}
}

func TestInputChangedRejectsResume(t *testing.T) {
	dir := t.TempDir()
	input := writeInput(t, dir, "in.webm", "original content")
	s, _ := openFor(t, dir, input, 2)
	if _, err := s.Commit(int64(len("original content")), 1, [][32]byte{{1}}); err != nil {
		t.Fatal(err)
	}
	// Mutate the input without updating the checkpoint.
	if err := os.WriteFile(input, []byte("CHANGED content!!"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Rebuild the store to pick up the new input identity.
	s2, _ := openFor(t, dir, input, 2)
	_, err := s2.Load()
	fe := fault.Of(err)
	if fe == nil || fe.Kind != fault.KindInputChanged || !fe.Recoverable {
		t.Fatalf("expected recoverable input-changed fault, got %v", err)
	}
	if fe.Offset < 0 {
		t.Fatal("input-changed fault must carry the checkpoint offset")
	}
}

func TestChecksumAccumulatesAcrossCommits(t *testing.T) {
	dir := t.TempDir()
	input := writeInput(t, dir, "in.webm", "abcd")
	s, _ := openFor(t, dir, input, 1)
	st1, err := s.Commit(1, 1, [][32]byte{{0xAA}})
	if err != nil {
		t.Fatal(err)
	}
	st2, err := s.Commit(2, 2, [][32]byte{{0xBB}})
	if err != nil {
		t.Fatal(err)
	}
	if st1.PayloadSHA256 == st2.PayloadSHA256 {
		t.Fatal("aggregate checksum must change as blocks are committed")
	}
	// Reload still validates after multiple commits.
	if _, err := s.Load(); err != nil {
		t.Fatalf("reload after multiple commits failed: %v", err)
	}
}

func TestInvalidBatchSize(t *testing.T) {
	dir := t.TempDir()
	input := writeInput(t, dir, "in.webm", "x")
	sum, _, _ := FileSHA256(input)
	if _, err := OpenStore(dir, 0, input, sum); !fault.Is(err, fault.KindCheckpointCorrupt) {
		t.Fatalf("expected batch-size fault, got %v", err)
	}
}
