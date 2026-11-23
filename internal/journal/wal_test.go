package journal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWALAppendReadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	w, err := CreateWAL(dir)
	if err != nil {
		t.Fatal(err)
	}
	recs := []Record{
		{Kind: RecBegin, Key: "seg-a"},
		{Kind: RecData, Key: "seg-a/f1.bin", Value: []byte{0xDE, 0xAD, 0xBE, 0xEF}},
		{Kind: RecCommit, Key: "seg-a"},
	}
	for _, rec := range recs {
		if err := w.Append(rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got, tail, err := ReadWAL(dir)
	if err != nil || tail != 0 || len(got) != len(recs) {
		t.Fatalf("read WAL: %+v tail=%d err=%v", got, tail, err)
	}
	for i, want := range recs {
		if got[i].Kind != want.Kind || got[i].Key != want.Key ||
			string(got[i].Value) != string(want.Value) {
			t.Fatalf("record %d mismatch: %+v", i, got[i])
		}
	}
}

func TestWALEmptyAndMissing(t *testing.T) {
	dir := t.TempDir()
	recs, _, err := ReadWAL(dir)
	if err != nil || recs != nil {
		t.Fatalf("missing WAL must read as empty: %v", err)
	}
	w, _ := CreateWAL(dir)
	w.Close()
	recs, _, err = ReadWAL(dir)
	if err != nil || len(recs) != 0 {
		t.Fatalf("empty WAL must have no records: %v", err)
	}
}

func TestWALIsolateTail(t *testing.T) {
	dir := t.TempDir()
	w, _ := CreateWAL(dir)
	if err := w.Append(Record{Kind: RecCommit, Key: "seg-b"}); err != nil {
		t.Fatal(err)
	}
	w.Close()
	path := filepath.Join(dir, walName)
	info, _ := os.Stat(path)
	good := info.Size()
	// Partial header: 4 bytes of what looks like a new record.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.Write([]byte{RecBegin, 0x00, 0x08, 0x00})
	f.Close()

	recs, tail, err := ReadWAL(dir)
	if err == nil {
		t.Fatal("partial record must surface a truncation error")
	}
	if tail != good || len(recs) != 1 {
		t.Fatalf("valid prefix wrong: recs=%d tail=%d good=%d", len(recs), tail, good)
	}
	if err := IsolateTail(dir, good); err != nil {
		t.Fatal(err)
	}
	recs, _, err = ReadWAL(dir)
	if err != nil || len(recs) != 1 {
		t.Fatalf("post-isolation WAL not clean: %v recs=%d", err, len(recs))
	}
}

func TestWALOversizedRejected(t *testing.T) {
	dir := t.TempDir()
	w, _ := CreateWAL(dir)
	defer w.Close()
	bigKey := make([]byte, 1<<16)
	if err := w.Append(Record{Kind: RecData, Key: string(bigKey)}); err == nil {
		t.Fatal("oversized key accepted")
	}
}
