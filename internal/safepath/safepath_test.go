package safepath

import (
	"strings"
	"testing"
)

func TestJoinUnderAllowsAndBlocks(t *testing.T) {
	root := t.TempDir()
	got, err := JoinUnder(root, "segments", "r0", "seg_000.m4s")
	if err != nil {
		t.Fatalf("valid join: %v", err)
	}
	if !strings.HasPrefix(got, root) {
		t.Fatalf("joined %q not under %q", got, root)
	}

	bad := []struct {
		elems []string
	}{
		{[]string{"..", "evil"}},
		{[]string{"segments", "..", "..", "evil"}},
		{[]string{"/etc/passwd"}},
	}
	for _, b := range bad {
		if _, err := JoinUnder(root, b.elems...); err == nil {
			t.Fatalf("expected rejection for %v", b.elems)
		}
	}
}

func TestValidateLocalOutput(t *testing.T) {
	ok := []string{"./tmp/demo", "/Users/test/out", "relative/dir", "file:///tmp/a.ts"}
	for _, p := range ok {
		if _, err := ValidateLocalOutput(p); err != nil {
			t.Fatalf("expected %q accepted: %v", p, err)
		}
	}
	bad := []string{"http://example.com/a", "https://x/y", "s3://bucket/k",
		"rtsp://h/live", `\\server\share`, `C:\Windows\x`, "user@host:/srv/out"}
	for _, p := range bad {
		if _, err := ValidateLocalOutput(p); err == nil {
			t.Fatalf("expected %q rejected", p)
		}
	}
	if _, err := ValidateLocalOutput("   "); err == nil {
		t.Fatal("empty path must be rejected")
	}
}

func TestMustRel(t *testing.T) {
	if _, err := MustRel("a", "../../b"); err == nil {
		t.Fatal("traversal must fail")
	}
	r, err := MustRel("segments", "r0", "a.m4s")
	if err != nil || r != "segments/r0/a.m4s" {
		t.Fatalf("MustRel=%q,%v", r, err)
	}
}
