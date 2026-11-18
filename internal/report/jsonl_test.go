package report

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jitterlab/internal/metrics"
)

func TestWritesFrameAndSummaryLines(t *testing.T) {
	var buf bytes.Buffer
	w, err := New(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFrame(metrics.Frame{Frame: 1, TimeMs: 100, Seq: 7, Kind: "real", BufferMs: 80, TargetMs: 80}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteSummary(metrics.Summary{Frames: 1, DroppedPackets: 2}); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}

	var frame FrameLine
	if err := json.Unmarshal([]byte(lines[0]), &frame); err != nil {
		t.Fatalf("frame line: %v", err)
	}
	if frame.Type != KindFrame || frame.Frame.BufferMs != 80 || frame.Frame.Seq != 7 {
		t.Fatalf("frame line = %+v", frame)
	}

	var summary SummaryLine
	if err := json.Unmarshal([]byte(lines[1]), &summary); err != nil {
		t.Fatalf("summary line: %v", err)
	}
	if summary.Type != KindSummary || summary.Summary.DroppedPackets != 2 {
		t.Fatalf("summary line = %+v", summary)
	}

	// Required top-level field names must survive on the wire.
	for _, field := range []string{`"buffer_ms"`, `"target_ms"`, `"input_packets"`, `"dropped_packets"`, `"concealed_frames"`, `"max_conceal_run"`, `"continuity_delta"`} {
		if !strings.Contains(buf.String(), field) {
			t.Errorf("output missing %s", field)
		}
	}
}

func TestNilWriterRejected(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("accepted nil writer")
	}
}

func TestCreateWritesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.jsonl")
	w, closer, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteSummary(metrics.Summary{Frames: 3}); err != nil {
		t.Fatal(err)
	}
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"frames":3`) {
		t.Fatalf("file content = %q", data)
	}
}

func TestCreateFailsForBadPath(t *testing.T) {
	dir := t.TempDir()
	_, _, err := Create(filepath.Join(dir, "missing", "out.jsonl"))
	if err == nil {
		t.Fatal("expected open error")
	}
}

// failingWriter fails after the first successful write.
type failingWriter struct{ writes int }

func (f *failingWriter) Write(p []byte) (int, error) {
	f.writes++
	if f.writes > 1 {
		return 0, errors.New("disk full")
	}
	return len(p), nil
}

func TestWriteErrorPropagates(t *testing.T) {
	fw := &failingWriter{}
	w, err := New(fw)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFrame(metrics.Frame{Frame: 1}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := w.WriteFrame(metrics.Frame{Frame: 2}); err == nil {
		t.Fatal("expected write error")
	}
}

var _ io.Writer = (*failingWriter)(nil)
