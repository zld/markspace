package manifest

import (
	"audionorm/internal/audio"
	"audionorm/internal/media"
	"audionorm/internal/task"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func spec() task.Spec {
	return task.Spec{SampleRate: 48000, Channels: 2, TargetRMS: -18, PeakCeilDB: -1}
}

func mkEntry(key, stream string, start, end uint64, status task.Status) Entry {
	return Entry{
		TaskKey: key, StreamID: stream, Start: start, End: end, Spec: spec(),
		Status: status, Samples: int(end - start), OutputSHA256: "abc123",
	}
}

func TestMergeSortsAndIsDeterministic(t *testing.T) {
	a := mkEntry("key-a", "s", 200, 300, task.StatusPASS)
	b := mkEntry("key-b", "s", 0, 100, task.StatusPARTIAL)
	c := mkEntry("key-c", "s", 100, 200, task.StatusHOLD)

	doc1, err := Merge(nil, a, b, c)
	if err != nil {
		t.Fatal(err)
	}
	// Different insertion order must yield the same sorted document.
	doc2, err := Merge(nil, c, a, b)
	if err != nil {
		t.Fatal(err)
	}
	h1, _ := Hash(doc1)
	h2, _ := Hash(doc2)
	if h1 != h2 {
		t.Fatal("merge is not order-independent")
	}
	if doc1.Entries[0].Start != 0 || doc1.Entries[2].Start != 200 {
		t.Fatal("entries not sorted by start time")
	}
}

func TestMergeIdempotentAndConflict(t *testing.T) {
	e := mkEntry("key-a", "s", 0, 100, task.StatusPASS)
	doc, err := Merge(nil, e)
	if err != nil {
		t.Fatal(err)
	}
	// Re-merging the identical entry changes nothing.
	doc2, err := Merge(doc, e)
	if err != nil || len(doc2.Entries) != 1 {
		t.Fatalf("identical re-merge must be a no-op, got %d entries err=%v", len(doc2.Entries), err)
	}
	hBefore, _ := Hash(doc)
	hAfter, _ := Hash(doc2)
	if hBefore != hAfter {
		t.Fatal("idempotent merge changed the hash")
	}
	// Same key, different content -> conflict, document untouched.
	conflict := e
	conflict.Status = task.StatusHOLD
	_, err = Merge(doc, conflict)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	if len(doc.Entries) != 1 || doc.Entries[0].Status != task.StatusPASS {
		t.Fatal("conflict must not overwrite the existing entry")
	}
}

func TestCanonicalSerializationStable(t *testing.T) {
	e := mkEntry("key-a", "s", 0, 100, task.StatusPASS)
	doc, _ := Merge(nil, e)
	b1, err := Serialize(doc)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := Serialize(doc)
	if string(b1) != string(b2) {
		t.Fatal("serialization is not stable")
	}
	if _, err := Hash(doc); err != nil {
		t.Fatal(err)
	}
}

func TestWriteFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "manifest.json")
	target := filepath.Join(dir, "target.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	doc, _ := Merge(nil, mkEntry("k", "s", 0, 10, task.StatusPASS))
	if _, err := WriteFile(link, doc); err == nil {
		t.Fatal("symlink manifest write must be rejected")
	}
}

func TestFromCheckpointProjection(t *testing.T) {
	r := media.Range{Start: 0, End: 960}
	key := task.StableKey("stream-x", r, spec())
	cp := &task.Checkpoint{
		TaskKey: key, StreamID: "stream-x", Range: r, Spec: spec(),
		Status: task.StatusPARTIAL, Samples: 480, MissingSamples: 480,
		DecoderVersion: audio.DecoderVersion,
	}
	e := FromCheckpoint(cp, "deadbeef")
	if e.TaskKey != key || e.OutputSHA256 != "deadbeef" || e.Status != task.StatusPARTIAL {
		t.Fatal("checkpoint projection lost fields")
	}
}
