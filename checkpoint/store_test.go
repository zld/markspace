package checkpoint

import (
	"os"
	"path/filepath"
	"testing"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cp.json")
	s, err := Open(path, NewFile("run1", "valid.mp4", "digest-a", 12))
	if err != nil {
		t.Fatal(err)
	}
	return s, path
}

func advance(s *Store, stage Stage, block int, input string) {
	s.Record(StageState{
		Stage: stage, InputDigest: input, CommittedBlock: block,
		Timestamp: uint32(block) * 8000, OutputDigest: "out",
	})
}

func TestRecordAndReload(t *testing.T) {
	s, path := newStore(t)
	advance(s, StageProbe, -1, "digest-a")
	advance(s, StageMetadata, -1, "digest-a")
	advance(s, StageAudio, 3, "digest-a")
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, NewFile("run1", "valid.mp4", "digest-a", 12))
	if err != nil {
		t.Fatal(err)
	}
	st, ok := reopened.Stage(StageAudio)
	if !ok || st.CommittedBlock != 3 || st.Timestamp != 24000 {
		t.Fatalf("reloaded stage: %+v ok=%t", st, ok)
	}
	if reopened.File().LastCommitted() != 3 {
		t.Fatalf("last committed %d", reopened.File().LastCommitted())
	}
}

func TestAtomicSaveLeavesNoTemp(t *testing.T) {
	s, path := newStore(t)
	advance(s, StageProbe, -1, "digest-a")
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}
}

func TestPlanResumesAfterCommittedBlock(t *testing.T) {
	f := NewFile("run1", "valid.mp4", "digest-a", 12)
	for _, s := range []Stage{StageProbe, StageMetadata, StageAudio, StageStream} {
		block := -1
		if s == StageAudio || s == StageStream {
			block = 3
		}
		f.Stages[s] = &StageState{Stage: s, InputDigest: "digest-a", CommittedBlock: block}
	}
	res, err := Plan(f, "digest-a")
	if err != nil {
		t.Fatal(err)
	}
	if res.FromStage != StageManifest {
		t.Fatalf("from stage %s want manifest", res.FromStage)
	}
	if res.StartBlock != 4 {
		t.Fatalf("start block %d want 4", res.StartBlock)
	}
}

func TestPlanRestartsOnInputChange(t *testing.T) {
	f := NewFile("run1", "valid.mp4", "digest-a", 12)
	f.Stages[StageProbe] = &StageState{Stage: StageProbe, InputDigest: "digest-a", CommittedBlock: -1}
	f.Stages[StageAudio] = &StageState{Stage: StageAudio, InputDigest: "digest-a", CommittedBlock: 5}
	res, err := Plan(f, "digest-b")
	if err != nil {
		t.Fatal(err)
	}
	if res.FromStage != StageProbe || res.StartBlock != 0 || !res.Recomputed {
		t.Fatalf("resume %+v", res)
	}
}

func TestPlanRestartsAtFailedStage(t *testing.T) {
	f := NewFile("run1", "valid.mp4", "digest-a", 12)
	f.Stages[StageProbe] = &StageState{Stage: StageProbe, InputDigest: "digest-a", CommittedBlock: -1}
	f.Stages[StageMetadata] = &StageState{Stage: StageMetadata, InputDigest: "digest-a", CommittedBlock: -1}
	f.Stages[StageAudio] = &StageState{Stage: StageAudio, InputDigest: "digest-a", CommittedBlock: 2}
	// Stream never completed; resume from stream's start with the audio
	// position preserved.
	res, err := Plan(f, "digest-a")
	if err != nil {
		t.Fatal(err)
	}
	if res.FromStage != StageStream {
		t.Fatalf("from stage %s want stream", res.FromStage)
	}
	if res.StartBlock != 3 {
		t.Fatalf("start block %d want 3", res.StartBlock)
	}
	if !res.Recomputed {
		t.Fatal("partially completed run must count as recomputed")
	}
}

func TestPlanRejectsBadBlock(t *testing.T) {
	f := NewFile("run1", "valid.mp4", "digest-a", 4)
	f.Stages[StageAudio] = &StageState{Stage: StageAudio, InputDigest: "digest-a", CommittedBlock: 9}
	if _, err := Plan(f, "digest-a"); err == nil {
		t.Fatal("out-of-range committed block must fail")
	}
}

func TestPlanRecomputeFlagRestartsStage(t *testing.T) {
	f := NewFile("run1", "valid.mp4", "digest-a", 12)
	f.Stages[StageProbe] = &StageState{Stage: StageProbe, InputDigest: "digest-a", CommittedBlock: -1}
	f.Stages[StageMetadata] = &StageState{Stage: StageMetadata, InputDigest: "digest-a", CommittedBlock: -1}
	f.Stages[StageAudio] = &StageState{Stage: StageAudio, InputDigest: "digest-a", CommittedBlock: 6, Recompute: true}
	res, err := Plan(f, "digest-a")
	if err != nil {
		t.Fatal(err)
	}
	if res.FromStage != StageAudio {
		t.Fatalf("from stage %s want audio", res.FromStage)
	}
	if res.StartBlock != 7 {
		t.Fatalf("start block %d", res.StartBlock)
	}
}

func TestOpenRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cp.json")
	if err := os.WriteFile(path, []byte("{oops"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, NewFile("r", "s", "i", 1)); err == nil {
		t.Fatal("corrupt checkpoint must fail")
	}
}

func TestSummaryStable(t *testing.T) {
	f := NewFile("run1", "valid.mp4", "d", 12)
	f.Stages[StageStream] = &StageState{Stage: StageStream, CommittedBlock: 3}
	f.Stages[StageAudio] = &StageState{Stage: StageAudio, CommittedBlock: 3}
	a := f.Summary()
	b := f.Summary()
	if a != b {
		t.Fatalf("unstable summary %q != %q", a, b)
	}
	if a != "run=run1 source=valid.mp4 blocks=12 audio:3 stream:3" {
		t.Fatalf("summary %q", a)
	}
}
