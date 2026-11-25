package checkpoint

import (
	"path/filepath"
	"testing"
)

func TestCommitsAppendAndReplace(t *testing.T) {
	f := NewFile("r", "s", "i", 4)
	f.AppendCommit(Commit{Block: 1, OutputDigest: "a"})
	f.AppendCommit(Commit{Block: 2, OutputDigest: "b"})
	f.AppendCommit(Commit{Block: 1, OutputDigest: "a2"})
	if len(f.Commits) != 2 {
		t.Fatalf("commits %d want 2", len(f.Commits))
	}
	got := f.CommitsThrough(1)
	if len(got) != 1 || got[0].OutputDigest != "a2" {
		t.Fatalf("commits through 1: %+v", got)
	}
}

func TestCommitsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cp.json")
	s, err := Open(path, NewFile("r", "s", "i", 3))
	if err != nil {
		t.Fatal(err)
	}
	s.File().AppendCommit(Commit{Block: 1, TrackID: 2, StartTick: 0, EndTick: 15000, Frames: 8000, OutputDigest: "d1"})
	s.File().AppendCommit(Commit{Block: 2, TrackID: 2, StartTick: 15000, EndTick: 30000, Frames: 8000, OutputDigest: "d2"})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	re, err := Open(path, NewFile("r", "s", "i", 3))
	if err != nil {
		t.Fatal(err)
	}
	if got := re.File().CommitsThrough(2); len(got) != 2 || got[1].OutputDigest != "d2" {
		t.Fatalf("reloaded commits: %+v", got)
	}
}

func TestRecomputedBlocks(t *testing.T) {
	f := NewFile("r", "s", "i", 12)
	cases := map[int]int{0: 12, 3: 10, 12: 1}
	for start, want := range cases {
		if got := f.RecomputedBlocks(start); got != want {
			t.Fatalf("start %d: got %d want %d", start, got, want)
		}
	}
}
