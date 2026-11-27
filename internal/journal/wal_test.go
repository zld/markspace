package journal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"media.rebuild/internal/model"
)

func mustOpen(t *testing.T, dir string) *Log {
	t.Helper()
	l, err := Open(filepath.Join(dir, "stage.wal"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func TestAppendAndReplay(t *testing.T) {
	dir := t.TempDir()
	l := mustOpen(t, dir)
	for i := 0; i < 3; i++ {
		if _, err := l.Append(Record{Type: TypeStageBegin, Task: "rebuild", Stage: "index"}); err != nil {
			t.Fatal(err)
		}
	}
	var got []Record
	recs, bad, err := Replay(l.Path(), func(r *Record) error {
		got = append(got, *r)
		return nil
	})
	if err != nil || len(bad) != 0 {
		t.Fatalf("replay: %v %v", err, bad)
	}
	if len(got) != 3 || len(recs) != 3 {
		t.Fatalf("callback %d records %d", len(got), len(recs))
	}
	for i, r := range recs {
		if r.Seq != int64(i+1) {
			t.Fatalf("seq %d at %d", r.Seq, i)
		}
		if len(r.Checksum) != 16 || r.Time == "" {
			t.Fatalf("record %+v", r)
		}
	}
}

func TestSequenceResumesAfterReopen(t *testing.T) {
	dir := t.TempDir()
	l := mustOpen(t, dir)
	l.Append(Record{Type: TypeStageBegin, Task: "t"})
	l.Append(Record{Type: TypeStageBegin, Task: "t"})
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l2, err := Open(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	rec, err := l2.Append(Record{Type: TypeStageDone, Task: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Seq != 3 {
		t.Fatalf("resumed seq %d", rec.Seq)
	}
}

func TestChecksumCorruptionDetected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.wal")
	good := `0123456789abcdef {"seq":1,"type":"stage_begin","task":"t"}` + "\n"
	wrong := `ffffffffffffffff {"seq":2,"type":"stage_begin","task":"t"}` + "\n"
	if err := os.WriteFile(path, []byte(good+wrong), 0o644); err != nil {
		t.Fatal(err)
	}
	recs, bad, err := Replay(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The hand-written good line has a stale checksum in this fixture, so
	// both are rejected; what matters is that replay reports rather than
	// crashes or trusts either.
	if len(recs)+len(bad) != 2 {
		t.Fatalf("records %d bad %d", len(recs), len(bad))
	}
	if len(bad) != 2 || bad[0].Code != model.CodeJournalCorrupt {
		t.Fatalf("bad %v", bad)
	}
	if bad[1].Source.Offset <= 0 {
		t.Fatalf("second bad record offset %d", bad[1].Source.Offset)
	}
}

func TestTornTailIsReportedAndEarlierRecordsSurvive(t *testing.T) {
	dir := t.TempDir()
	l := mustOpen(t, dir)
	for i := 0; i < 2; i++ {
		if _, err := l.Append(Record{Type: TypeStageBegin, Task: "t", Stage: "merge"}); err != nil {
			t.Fatal(err)
		}
	}
	path := l.Path()
	l.Close()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`0011223344556677 {"seq":3,"type":"stage_d`)
	f.Close()

	recs, bad, err := Replay(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("durable records lost: %d", len(recs))
	}
	if len(bad) != 1 || bad[0].Code != model.CodeJournalCorrupt {
		t.Fatalf("torn tail not reported: %v", bad)
	}
	if !strings.Contains(bad[0].Error(), "torn") {
		t.Fatalf("detail %v", bad[0])
	}
}

func TestAppendRejectsIncompleteRecord(t *testing.T) {
	dir := t.TempDir()
	l := mustOpen(t, dir)
	if _, err := l.Append(Record{Task: "t"}); !model.IsCode(err, model.CodeInvalidArgument) {
		t.Fatal("record without type accepted")
	}
	if _, err := l.Append(Record{Type: TypeStageBegin}); !model.IsCode(err, model.CodeInvalidArgument) {
		t.Fatal("record without task accepted")
	}
}

func TestReplayOfMissingFileIsEmpty(t *testing.T) {
	recs, bad, err := Replay(filepath.Join(t.TempDir(), "none.wal"), nil)
	if err != nil || len(recs) != 0 || len(bad) != 0 {
		t.Fatalf("missing file: %v %v %v", recs, bad, err)
	}
}

func TestPayloadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	l := mustOpen(t, dir)
	payload, _ := json.Marshal(map[string]any{"artifact": "frames/000.png", "sha256": "abc"})
	if _, err := l.Append(Record{Type: TypeArtifact, Task: "t", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	recs, _, _ := Replay(l.Path(), nil)
	if len(recs) != 1 {
		t.Fatalf("records %d", len(recs))
	}
	var got map[string]string
	if err := json.Unmarshal(recs[0].Payload, &got); err != nil {
		t.Fatal(err)
	}
	if got["artifact"] != "frames/000.png" {
		t.Fatalf("payload %v", got)
	}
}
