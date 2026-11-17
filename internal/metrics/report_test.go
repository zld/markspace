package metrics

import (
	"strings"
	"testing"
)

func TestJumpP95(t *testing.T) {
	s := New(100)
	for i := 1; i <= 100; i++ {
		s.NoteJump(i)
	}
	if got := s.JumpP95(); got != 95 {
		t.Fatalf("p95=%d, want 95", got)
	}
	if got := New(100).JumpP95(); got != 0 {
		t.Fatalf("empty p95=%d, want 0", got)
	}
}

func TestPLCRatioAndRuns(t *testing.T) {
	s := New(100)
	for i := 0; i < 3; i++ {
		s.NotePLC()
	}
	s.NoteReal()
	s.NotePLC()
	if s.MaxPLCRun != 3 {
		t.Fatalf("max run=%d, want 3", s.MaxPLCRun)
	}
	if got := s.PLCRatio(); got != 0.8 {
		t.Fatalf("ratio=%v, want 0.8", got)
	}
}

func TestCurveBuckets(t *testing.T) {
	s := New(100)
	for i := 0; i < 10; i++ {
		s.Observe(int64(i*10), 4) // bucket 0
	}
	s.Observe(100, 8) // bucket 1
	rows := s.Curve()
	if len(rows) != 2 {
		t.Fatalf("rows=%d, want 2", len(rows))
	}
	if rows[0].TimeMs != 0 || rows[0].QueueFrames != 4 {
		t.Fatalf("row0=%+v", rows[0])
	}
	if rows[1].TimeMs != 100 || rows[1].QueueFrames != 8 {
		t.Fatalf("row1=%+v", rows[1])
	}
}

func TestWriteTextContainsFields(t *testing.T) {
	s := New(100)
	s.Input = 10
	s.NetworkLost = 2
	s.EndReason = "trace_complete"
	s.Observe(0, 3)
	var b strings.Builder
	if err := s.WriteText(&b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"input=10", "network_lost=2", "duplicate_drop=", "late_drop=",
		"overflow_drop=", "drop_total=", "plc_frames=", "plc_ratio=",
		"max_plc_run=", "resume_jump_p95=", "time_ms,queue_frames",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}
