package checkpoint

import (
	"os"
	"path/filepath"
	"testing"
)

func rec(block int, first int) Record {
	return Record{Block: block, TrackID: 1, StartNS: int64(first) * 100, EndNS: int64(first+10) * 100,
		FirstSample: first, Samples: 10, Content: "abc", KeyExact: true}
}

func TestAppendReplayAndCompleted(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Append(rec(0, 0), rec(0, 10)); err != nil {
		t.Fatal(err)
	}
	if err := st.Append(rec(1, 20)); err != nil {
		t.Fatal(err)
	}
	if err := st.Commit(); err != nil {
		t.Fatal(err)
	}
	if !st.Completed(0) || !st.Completed(1) || st.Completed(2) {
		t.Fatal("Completed disagreed")
	}
	if st.NextBlock() != 2 {
		t.Fatalf("next block=%d", st.NextBlock())
	}
	st.Close()

	st2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if len(st2.Records()) != 3 || st2.NextBlock() != 2 || st2.Recovered() {
		t.Fatalf("replay: records=%d next=%d torn=%v", len(st2.Records()), st2.NextBlock(), st2.Recovered())
	}
	if !st2.Completed(1) || len(st2.BlockRecords(0)) != 2 {
		t.Fatal("block contents lost across reopen")
	}
}

func TestTornCheckpointTailIsDiscarded(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir)
	if err := st.Append(rec(0, 0)); err != nil {
		t.Fatal(err)
	}
	if err := st.Commit(); err != nil {
		t.Fatal(err)
	}
	st.Close()
	// A half-written record: header claims 200 bytes, only 10 exist.
	f, err := os.OpenFile(filepath.Join(dir, "journal.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte{0, 0, 0, 200, 0xde, 0xad, 0xbe, 0xef, '{', '"', 'b'})
	f.Close()

	st2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if !st2.Recovered() {
		t.Fatal("torn tail not reported")
	}
	if len(st2.Records()) != 1 || !st2.Completed(0) {
		t.Fatalf("torn tail damaged replayed records: %+v", st2.Records())
	}
	// The unparseable suffix must be gone, or later appends would be invisible.
	assertJournalClean(t, filepath.Join(dir, "journal.jsonl"))
	if err := st2.Append(rec(1, 10)); err != nil {
		t.Fatal(err)
	}
	if err := st2.Commit(); err != nil {
		t.Fatal(err)
	}
	st2.Close()
	st3, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st3.Close()
	if st3.Recovered() {
		t.Fatal("second open still sees a torn record")
	}
	if len(st3.Records()) != 2 || !st3.Completed(1) {
		t.Fatalf("second replay: %+v", st3.Records())
	}
}

func assertJournalClean(t *testing.T, path string) {
	t.Helper()
	recs, torn, _, err := readJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if torn {
		t.Fatal("journal still has a torn tail after repair")
	}
	if len(recs) != 1 {
		t.Fatalf("journal records=%d", len(recs))
	}
}

func TestCheckpointFileCorruptionIsFatal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "checkpoint.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Open(dir)
	if err == nil {
		t.Fatal("corrupt checkpoint accepted")
	}
	if code := exitOf(err); code != 2 {
		t.Fatalf("exit=%d want 2", code)
	}
}

func TestCommitIsAtomicReplace(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir)
	defer st.Close()
	st.Append(rec(0, 0))
	if err := st.Commit(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "checkpoint.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(body) == 0 || body[0] != '{' {
		t.Fatalf("checkpoint not JSON: %q", body)
	}
	if _, err := os.Stat(filepath.Join(dir, "checkpoint.json.tmp")); !os.IsNotExist(err) {
		t.Fatal("temporary checkpoint file left behind")
	}
}

func TestReplayOrderIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Same block, interleaved identities: map iteration used to randomise
	// this ordering between opens.
	for _, first := range []int{30, 10, 20} {
		if err := st.Append(rec(0, first)); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Commit(); err != nil {
		t.Fatal(err)
	}
	st.Close()
	want := []int{10, 20, 30}
	for attempt := 0; attempt < 5; attempt++ {
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		recs := s.Records()
		s.Close()
		if len(recs) != len(want) {
			t.Fatalf("records=%d", len(recs))
		}
		for i, w := range want {
			if recs[i].FirstSample != w {
				t.Fatalf("attempt %d order=%v", attempt, recs)
			}
		}
	}
}
