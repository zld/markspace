package repair

import (
	"errors"
	"io"
	"testing"

	"mediad/internal/merr"
	"mediad/internal/testkit"
)

func TestIndexPointsOnlyAtKeyframes(t *testing.T) {
	doc, err := testkit.BuildDoc(testkit.DemoDocSpec(5))
	if err != nil {
		t.Fatal(err)
	}
	raw, tracks, err := demux(t, doc.Data)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("demux: %v", err)
	}
	res, err := Repair(raw, tracks, Options{})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := BuildIndex(res.Blocks, IndexOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Index.Entries) < 10 {
		t.Fatalf("only %d index entries", len(idx.Index.Entries))
	}
	if err := EnsureKeyframesOnly(idx.Index, res.Blocks); err != nil {
		t.Fatal(err)
	}
	// Every entry must point at real bytes in the document.
	for i, e := range idx.Index.Entries {
		if e.ByteOffset < 0 || e.ByteOffset >= int64(len(doc.Data)) {
			t.Fatalf("entry %d offset %d outside document", i, e.ByteOffset)
		}
	}
}

func TestIndexDigestIsRepeatable(t *testing.T) {
	doc, err := testkit.BuildDoc(testkit.DemoDocSpec(9))
	if err != nil {
		t.Fatal(err)
	}
	build := func() IndexResult {
		raw, tracks, err := demux(t, doc.Data)
		if !errors.Is(err, io.EOF) {
			t.Fatalf("demux: %v", err)
		}
		res, err := Repair(raw, tracks, Options{})
		if err != nil {
			t.Fatal(err)
		}
		idx, err := BuildIndex(res.Blocks, IndexOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return idx
	}
	a, b := build(), build()
	if a.Index.Digest != b.Index.Digest {
		t.Fatalf("index digest differs: %s vs %s", a.Index.Digest, b.Index.Digest)
	}
	if len(a.Index.Entries) != len(b.Index.Entries) {
		t.Fatalf("entry counts differ: %d vs %d", len(a.Index.Entries), len(b.Index.Entries))
	}
}

func TestDamagedInputKeepsConfirmedPrefixAndCursor(t *testing.T) {
	fixture, err := testkit.BuildFaulted(testkit.FaultClusterTruncate, 4)
	if err != nil {
		t.Fatal(err)
	}
	raw, tracks, readErr := demux(t, fixture.Data)
	if readErr == nil || errors.Is(readErr, io.EOF) {
		t.Fatalf("expected a truncation error, got %v", readErr)
	}
	if len(raw) == 0 {
		t.Fatal("no blocks confirmed before truncation")
	}
	if code := merr.CodeOf(readErr); code != merr.CodeTruncatedCluster {
		t.Fatalf("error code = %s, want truncated_cluster", code)
	}
	res, err := Repair(raw, tracks, Options{})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := BuildIndexFromRepair(res, readErr, IndexOptions{})
	if err != nil && !merr.IsDamage(merr.CodeOf(err)) {
		t.Fatalf("unexpected non-damage error: %v", err)
	}
	if idx.Cursor.ConfirmedBytes <= 0 {
		t.Fatalf("cursor has no confirmed prefix: %s", idx.Cursor)
	}
	if idx.Cursor.ConfirmedBytes > int64(len(fixture.Data)) {
		t.Fatalf("cursor %d past end of input %d", idx.Cursor.ConfirmedBytes, len(fixture.Data))
	}
	if !idx.Cursor.Damaged {
		t.Fatal("cursor not marked damaged")
	}
	if idx.Cursor.Cause != merr.CodeTruncatedCluster {
		t.Fatalf("cursor cause = %s", idx.Cursor.Cause)
	}
	if len(idx.Index.Entries) == 0 {
		t.Fatal("damaged input produced no index prefix")
	}
}

func TestResumeFromCursorReproducesPrefix(t *testing.T) {
	fixture, err := testkit.BuildFaulted(testkit.FaultClusterTruncate, 6)
	if err != nil {
		t.Fatal(err)
	}
	raw, tracks, readErr := demux(t, fixture.Data)
	if readErr == nil {
		t.Fatal("expected truncation")
	}
	res, err := Repair(raw, tracks, Options{})
	if err != nil {
		t.Fatal(err)
	}
	first, _ := BuildIndexFromRepair(res, readErr, IndexOptions{})

	// Re-reading only the confirmed prefix must reproduce the same index
	// digest: that is what makes a resumed run auditable.
	prefix := fixture.Data[:first.Cursor.ConfirmedBytes]
	raw2, tracks2, err2 := demux(t, prefix)
	// The cut prefix ends inside the enclosing Segment/Cluster masters, so
	// the walker reports truncation at EOF. That is expected: the cursor
	// marks where reading stopped, not a self-contained document.
	if err2 != nil && !errors.Is(err2, io.EOF) {
		if code := merr.CodeOf(err2); !merr.IsDamage(code) {
			t.Fatalf("prefix re-read failed with a non-damage error: %v", err2)
		}
	}
	res2, err := Repair(raw2, tracks2, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Blocks) != len(res.Blocks) {
		t.Fatalf("resumed blocks = %d, first = %d", len(res2.Blocks), len(res.Blocks))
	}
	second, err := BuildIndex(res2.Blocks, IndexOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Index.Entries) != len(first.Index.Entries) {
		t.Fatalf("resumed entries = %d, first = %d",
			len(second.Index.Entries), len(first.Index.Entries))
	}
	if second.Index.Digest != first.Index.Digest {
		t.Fatalf("resumed digest differs: %s vs %s", second.Index.Digest, first.Index.Digest)
	}
}
