package metrics

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestRecorderInterval(t *testing.T) {
	r := NewRecorder(200)
	r.Record("udp", 0, 3, 2)
	r.Record("udp", 100*time.Millisecond, 4, 2)  // too soon, skipped
	r.Record("udp", 250*time.Millisecond, 5, 3)  // recorded
	r.Record("trace", 250*time.Millisecond, 1, 2) // other path independent
	if len(r.points) != 3 {
		t.Fatalf("points = %d, want 3", len(r.points))
	}
	if r.points[1].ElapsedMs != 250 || r.points[1].Queued != 5 {
		t.Fatalf("point[1] = %+v", r.points[1])
	}
}

func TestWriteCSV(t *testing.T) {
	r := NewRecorder(200)
	r.Record("udp", 0, 3, 2)
	r.Record("trace", 200*time.Millisecond, 1, 4)
	var buf bytes.Buffer
	if err := r.WriteCSV(&buf); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("csv lines = %d, want 3", len(lines))
	}
	if lines[0] != "path,elapsed_ms,queued_frames,target_frames" {
		t.Fatalf("header = %q", lines[0])
	}
	if lines[1] != "udp,0,3,2" || lines[2] != "trace,200,1,4" {
		t.Fatalf("rows = %q / %q", lines[1], lines[2])
	}
}

func TestWriteSummaries(t *testing.T) {
	var buf bytes.Buffer
	sums := []Summary{
		{Path: "udp", FramesOut: 600, DropsDuplicate: 3, DropsLate: 1, ConcealedFrames: 25, ContinuityOKRatio: 0.99, MaxBoundaryJump: 1200},
		{Path: "trace", FramesOut: 600},
	}
	if err := WriteSummaries(&buf, sums); err != nil {
		t.Fatalf("WriteSummaries: %v", err)
	}
	out := buf.String()
	for _, field := range []string{`"frames_out":600`, `"drops_duplicate":3`, `"drops_late":1`, `"concealed_frames":25`, `"continuity_ok_ratio":0.99`, `"max_boundary_jump":1200`} {
		if !strings.Contains(out, field) {
			t.Fatalf("summary output missing %s:\n%s", field, out)
		}
	}
	if strings.Count(strings.TrimSpace(out), "\n") != 1 {
		t.Fatalf("want 2 JSON lines, got:\n%s", out)
	}
}
