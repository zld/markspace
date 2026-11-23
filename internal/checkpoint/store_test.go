package checkpoint

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"archivecheck/internal/model"
	"archivecheck/internal/scheduler"
)

func testKey(id string) scheduler.Key {
	return scheduler.Key{NodeID: id, InputDigest: "in-" + id, ParamsDigest: "p"}
}

func TestLoadMissingIsEmpty(t *testing.T) {
	s := New(t.TempDir())
	st, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Entries) != 0 || st.Version != Version {
		t.Fatalf("state %+v", st)
	}
}

func TestSaveLoadRoundTripIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	entries := Add(nil, testKey("b"), scheduler.Artifact{Digest: "db", Size: 20}, fixed)
	entries = Add(entries, testKey("a"), scheduler.Artifact{Digest: "da", Size: 10}, fixed)
	if err := s.Save(entries); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(st.Entries) != 2 {
		t.Fatalf("entries %+v", st.Entries)
	}
	if !sort.SliceIsSorted(st.Entries, func(i, j int) bool { return st.Entries[i].Key < st.Entries[j].Key }) {
		t.Fatalf("entries are not key-ordered: %+v", st.Entries)
	}
	// Saving the same logical state again yields byte-identical output.
	if err := s.Save(entries); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("checkpoint bytes are not deterministic")
	}
}

func TestCompletedMapFeedsScheduler(t *testing.T) {
	entries := Add(nil, testKey("a"), scheduler.Artifact{Digest: "da", Size: 1}, time.Now())
	m := Completed(entries)
	if m[testKey("a").Digest()].Digest != "da" {
		t.Fatalf("completed %+v", m)
	}
}

func TestAddReplacesSameKey(t *testing.T) {
	k := testKey("a")
	entries := Add(nil, k, scheduler.Artifact{Digest: "v1", Size: 1}, time.Now())
	entries = Add(entries, k, scheduler.Artifact{Digest: "v2", Size: 2}, time.Now())
	if len(entries) != 1 || entries[0].Artifact.Digest != "v2" {
		t.Fatalf("entries %+v", entries)
	}
}

func TestCorruptCheckpointIsDetected(t *testing.T) {
	cases := map[string]func([]byte) []byte{
		"truncated json": func(b []byte) []byte { return b[:len(b)/2] },
		"bad digest": func(b []byte) []byte {
			var st State
			if err := json.Unmarshal(b, &st); err != nil {
				t.Fatal(err)
			}
			st.Digest = "deadbeef"
			out, _ := json.Marshal(st)
			return out
		},
		"tampered entry": func(b []byte) []byte {
			var st State
			if err := json.Unmarshal(b, &st); err != nil {
				t.Fatal(err)
			}
			st.Entries[0].Artifact.Digest = "evil"
			out, _ := json.Marshal(st)
			return out
		},
		"wrong version": func(b []byte) []byte {
			var st State
			if err := json.Unmarshal(b, &st); err != nil {
				t.Fatal(err)
			}
			st.Version = Version + 1
			out, _ := json.Marshal(st)
			return out
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			s := New(dir)
			entries := Add(nil, testKey("a"), scheduler.Artifact{Digest: "da", Size: 1}, time.Now())
			if err := s.Save(entries); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(s.Path())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(s.Path(), mutate(raw), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err = s.Load()
			if err == nil {
				t.Fatal("corrupt checkpoint must be rejected")
			}
			f, ok := model.AsFault(err)
			if !ok {
				t.Fatalf("want fault, got %v", err)
			}
			switch name {
			case "wrong version":
				if f.Code != model.CodeCheckpointVersion {
					t.Fatalf("code %s", f.Code)
				}
			default:
				if f.Code != model.CodeCheckpointCorrupt {
					t.Fatalf("code %s", f.Code)
				}
			}
		})
	}
}

func TestResumeOnlyRerunsUnconfirmedNodes(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	// Simulate an interrupted run: two nodes confirmed, a third never saved.
	entries := Add(nil, testKey("probe"), scheduler.Artifact{Digest: "dp", Size: 1}, time.Now())
	entries = Add(entries, testKey("inspect-1"), scheduler.Artifact{Digest: "d1", Size: 2}, time.Now())
	if err := s.Save(entries); err != nil {
		t.Fatal(err)
	}

	state, err := New(dir).Load()
	if err != nil {
		t.Fatalf("resume after interruption must succeed: %v", err)
	}
	done := Completed(state.Entries)
	var ran []string
	run := func(id string) {
		ran = append(ran, id)
	}
	for _, id := range []string{"probe", "inspect-1", "inspect-2"} {
		if _, ok := done[testKey(id).Digest()]; ok {
			continue
		}
		run(id)
	}
	if len(ran) != 1 || ran[0] != "inspect-2" {
		t.Fatalf("resumed nodes %v", ran)
	}
}

func TestAtomicWriteLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	for i := 0; i < 5; i++ {
		entries := Add(nil, testKey("a"), scheduler.Artifact{Digest: "d", Size: int64(i)}, time.Now())
		if err := s.Save(entries); err != nil {
			t.Fatal(err)
		}
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, de := range des {
		if de.Name() == FileName {
			continue
		}
		if filepath.Ext(de.Name()) == ".tmp" {
			t.Fatalf("leftover temp file %s", de.Name())
		}
	}
}
