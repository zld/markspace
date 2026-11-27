package fingerprint

import (
	"os"
	"path/filepath"
	"testing"

	"mediabatch/internal/clock"
	"mediabatch/internal/fault"
)

func rec(sha string, start, end int64, size int64, rep string) Record {
	return Record{
		ContentSHA: sha, Size: size,
		Start: clock.MustNew(start, 1), End: clock.MustNew(end, 1),
		Manifest: "m", Representation: rep, Segment: rep + ".m4s",
	}
}

func TestCrossBitrateMerge(t *testing.T) {
	ix := Build([]Record{
		rec("abc", 0, 4, 100, "v720"),
		rec("abc", 0, 4, 100, "v360"),
		rec("def", 4, 8, 120, "v720"),
	})
	if len(ix.Groups) != 2 {
		t.Fatalf("groups=%d", len(ix.Groups))
	}
	if ix.Duplicates() != 1 {
		t.Fatalf("duplicates=%d", ix.Duplicates())
	}
	if ix.Issues.Errors() != 0 {
		t.Fatalf("unexpected issues: %+v", ix.Issues)
	}
	// Deterministic canonical pick: by start then representation.
	if ix.Groups[0].Canonical.Representation != "v360" {
		t.Fatalf("canonical=%s", ix.Groups[0].Canonical.Representation)
	}
}

func TestTimeOrSizeMismatchIsNotDuplicate(t *testing.T) {
	ix := Build([]Record{
		rec("same", 0, 4, 100, "a"),
		rec("same", 4, 8, 100, "b"), // same bytes, wrong time
		rec("same", 0, 4, 101, "c"), // same bytes, wrong size
	})
	if ix.Duplicates() != 0 {
		t.Fatalf("content at a different time or size must not merge: %d", ix.Duplicates())
	}
	codes := map[fault.Code]int{}
	for _, i := range ix.Issues {
		codes[i.Code]++
		if i.Severity != fault.SeverityError {
			t.Fatalf("drift must be an error: %+v", i)
		}
		if i.Source.Loc.File == "" {
			t.Fatalf("drift issue lacks a source: %+v", i)
		}
	}
	if codes[fault.CodeFingerprintDrift] == 0 {
		t.Fatalf("expected drift issues, got %+v", ix.Issues)
	}
	if len(ix.Rejected) == 0 {
		t.Fatal("expected rejected records")
	}
}

func TestEmptyHashSkipped(t *testing.T) {
	ix := Build([]Record{rec("", 0, 4, 1, "a"), rec("x", 0, 4, 1, "b")})
	if len(ix.Groups) != 1 || ix.Duplicates() != 0 {
		t.Fatalf("groups=%d dup=%d", len(ix.Groups), ix.Duplicates())
	}
}

func TestHashFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.bin")
	os.WriteFile(p, []byte("hello"), 0o644)
	sum, n, err := HashFile(p)
	if err != nil || n != 5 || len(sum) != 64 {
		t.Fatalf("sum=%s n=%d err=%v", sum, n, err)
	}
	if _, _, err := HashFile(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("expected error")
	}
	// Same bytes => same key, so a rerun converges on one artifact.
	a := rec(sum, 0, 4, n, "a")
	b := rec(sum, 0, 4, n, "b")
	if a.Key() != b.Key() {
		t.Fatal("identical content, timing and size must share a key")
	}
}
