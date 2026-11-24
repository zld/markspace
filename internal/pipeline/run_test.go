package pipeline

import (
	"path/filepath"
	"testing"

	"medcheck/internal/clock"
	"medcheck/internal/sample"
)

func testPaths(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "checkpoint.jsonl"), filepath.Join(dir, "out.mp4")
}

func TestResumeMatchesCompleteRun(t *testing.T) {
	cp, out := testPaths(t)
	resume, err := RunDemo(Config{
		Seed: 17, TruncateAt: 7, Resume: true, CheckpointEvery: 1,
		CheckpointPath: cp, OutputPath: out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resume.ExitCode != 0 || resume.ResumedBoundary != 7 {
		t.Fatalf("resume result=%+v", resume)
	}

	fullCP, fullOut := testPaths(t)
	full, err := RunDemo(Config{Seed: 17, CheckpointEvery: 5, CheckpointPath: fullCP, OutputPath: fullOut})
	if err != nil {
		t.Fatal(err)
	}
	if resume.SHA256 != full.SHA256 {
		t.Fatalf("resume sha=%s full sha=%s", resume.SHA256, full.SHA256)
	}
}

func TestLossFirstBadOffsetIsWithinOnePacketBoundary(t *testing.T) {
	cp, out := testPaths(t)
	result, err := RunDemo(Config{
		Seed: 17, Loss: []int{4, 9}, Reorder: 3, CheckpointEvery: 5,
		CheckpointPath: cp, OutputPath: out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reordered != 3 || result.Recovered == 0 {
		t.Fatalf("unexpected recovery stats: %+v", result)
	}
	generated, err := sample.Generate(sample.Config{Seed: 17, Loss: []int{4, 9}, Reorder: 3})
	if err != nil {
		t.Fatal(err)
	}
	var actual int64 = -1
	for _, p := range generated.All {
		if p.Global == 4 {
			actual = p.Offset
		}
	}
	diff := result.FirstBadOffset - actual
	if diff < 0 {
		diff = -diff
	}
	if diff > sample.SlotSize {
		t.Fatalf("first_bad_offset=%d actual=%d diff=%d > one packet", result.FirstBadOffset, actual, diff)
	}
}

func TestCorruptCheckpointFallsBackToNearestBoundary(t *testing.T) {
	cp, out := testPaths(t)
	result, err := RunDemo(Config{
		Seed: 17, TruncateAt: 7, Resume: true, CheckpointEvery: 1,
		CorruptCheckpoint: true, CheckpointPath: cp, OutputPath: out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ResumedBoundary != 6 {
		t.Fatalf("boundary=%d want 6", result.ResumedBoundary)
	}
	if result.ErrorCodes[string(clock.CodeCheckpointInvalid)] != 1 {
		t.Fatalf("error codes=%v", result.ErrorCodes)
	}
	if result.SHA256 == "" {
		t.Fatal("missing output checksum")
	}
}

func TestUnrecoverableVideoGapReturnsNonZeroCode(t *testing.T) {
	cp, out := testPaths(t)
	result, err := RunDemo(Config{
		Seed: 17, Unrecoverable: true, CheckpointPath: cp, OutputPath: out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 2 {
		t.Fatalf("exit code=%d codes=%v", result.ExitCode, result.ErrorCodes)
	}
	if result.ErrorCodes[string(clock.CodeVideoUnrecoverable)] == 0 {
		t.Fatalf("missing unrecoverable code: %v", result.ErrorCodes)
	}
}

func TestGeneratedEdgeCasesAllHandled(t *testing.T) {
	for _, c := range RunCases() {
		if !c.OK {
			t.Fatalf("case %s not handled: %+v", c.Name, c)
		}
		if c.ErrorCode == "" || len(c.ErrorCodes) == 0 {
			t.Fatalf("case %s missing error code/counts", c.Name)
		}
	}
}
