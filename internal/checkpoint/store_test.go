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
	st.Close()

	st2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if len(st2.Records()) != 3 || st2.NextBlock() != 2 || st2.Recovered() {
		t.Fatalf("replay: records=%d next=%d torn=%v", len(st2.Records()), st2.NextBlock(), st2.Recovered())
	}
	if !st2.Completed(1) {
		t.Fatal("block 1 should be committed after replay")
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
	// Simulate a half-written record: header claims 200 bytes, only 5 exist.
	f, err := os.OpenFile(filepath.Join(dir, "journal.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte{0, 0, 0, 200, 0xde, 0xad, 0xbe, 0xef, '{', '"'})
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
		t.Fatalf("torn tail corrupted replayed records: %+v", st2.Records())
	}
	// Appending after a torn tail must remain durable and replayable.
	if err := st2.Append(rec(1, 10)); err != nil {
		t.Fatal(err)
	}
	if err := st2.Commit(); err != nil {
		t.Fatal(err)
	}
	st2.Close()
	st3, _ := Open(dir)
	defer st3.Close()
	if st3.Recovered() {
		t.Fatal("second open should treat the torn record as isolated")
	}
	if len(st3.Records()) != 2 || !st3.Completed(1) {
		t.Fatalf("second replay: %+v", st3.Records())
	}
}

func TestCheckpointFileCorruptionIsFatal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "checkpoint.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("corrupt checkpoint accepted")
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
