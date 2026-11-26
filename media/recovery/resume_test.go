package recovery

import (
	"os"
	"path/filepath"
	"testing"

	"replaykit/media/audit"
)

func testItems(n int) []Item {
	items := make([]Item, n)
	for i := range items {
		body := []byte{byte('a' + i), byte(i), 0, 1, 2, 3, 4, 5}
		items[i] = Item{
			Key:         fmtKey(i),
			File:        fileName(i),
			InputDigest: "input-" + string(rune('a'+i)),
			RangeStartNS: int64(i) * 1000,
			RangeEndNS:   int64(i+1) * 1000,
			Hash:        audit.HashBytes(body),
			Bytes:       body,
		}
	}
	return items
}

func fmtKey(i int) string  { return "event/layer-" + string(rune('0'+i)) }
func fileName(i int) string { return "seg-" + string(rune('0'+i)) + ".fmp4" }

func TestResumeBuildReuseRebuild(t *testing.T) {
	dir := t.TempDir()
	items := testItems(6)

	first, err := Run(dir, items)
	if err != nil {
		t.Fatal(err)
	}
	if first.Built != 6 || first.Reused != 0 || first.Rebuilt != 0 {
		t.Fatalf("first run built=%d reused=%d rebuilt=%d", first.Built, first.Reused, first.Rebuilt)
	}
	if first.DuplicateProducts != 0 {
		t.Fatalf("duplicates=%d want 0", first.DuplicateProducts)
	}

	// Unchanged rerun reuses all six.
	second, err := Run(dir, items)
	if err != nil {
		t.Fatal(err)
	}
	if second.Reused != 6 || second.Rebuilt != 0 {
		t.Fatalf("clean rerun reused=%d rebuilt=%d want 6/0", second.Reused, second.Rebuilt)
	}

	// Corrupt exactly one output; rerun must rebuild just that one and reuse 5.
	target := segmentPath(dir, items[2].File)
	if err := os.WriteFile(target, []byte("CORRUPT-BYTES-HERE"), 0o644); err != nil {
		t.Fatal(err)
	}
	third, err := Run(dir, items)
	if err != nil {
		t.Fatal(err)
	}
	if third.Rebuilt != 1 {
		t.Fatalf("rebuilt=%d want 1", third.Rebuilt)
	}
	if third.Reused != 5 {
		t.Fatalf("reused=%d want 5", third.Reused)
	}
	if !third.MatchedAll() || third.HashMatched != 6 {
		t.Fatalf("hash match=%d/%d want 6/6", third.HashMatched, third.HashTotal)
	}
	if third.DuplicateProducts != 0 {
		t.Fatalf("duplicate products=%d want 0", third.DuplicateProducts)
	}

	// Exactly six canonical files remain; no "(1)" duplicates.
	entries, _ := os.ReadDir(filepath.Join(dir, "segments"))
	if len(entries) != 6 {
		t.Fatalf("segment files=%d want 6", len(entries))
	}
}

func TestVerifyDetectsTamperWithoutRebuild(t *testing.T) {
	dir := t.TempDir()
	items := testItems(2)
	if _, err := Run(dir, items); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(segmentPath(dir, items[0].File), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	matched, bad := Verify(dir, items)
	if matched != 1 || len(bad) != 1 || bad[0] != items[0].Key {
		t.Fatalf("verify matched=%d bad=%v", matched, bad)
	}
}

func TestInputDigestChangeForcesRebuild(t *testing.T) {
	dir := t.TempDir()
	items := testItems(3)
	if _, err := Run(dir, items); err != nil {
		t.Fatal(err)
	}
	// Change one item's bound input digest while keeping bytes; identity no
	// longer matches so the checkpoint row is not reused.
	items[1].InputDigest = "different-input"
	// Hash still equals bytes; but identity mismatch -> rewrite as rebuilt.
	res, err := Run(dir, items)
	if err != nil {
		t.Fatal(err)
	}
	if res.Reused != 2 || res.Rebuilt != 1 {
		t.Fatalf("after input change reused=%d rebuilt=%d want 2/1", res.Reused, res.Rebuilt)
	}
}
