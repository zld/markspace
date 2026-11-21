package validate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"streamcheck/internal/manifest"
)

func TestSafeJoin(t *testing.T) {
	dir := t.TempDir()
	for _, bad := range []string{"", "..", "../x.ts", "/etc/passwd", "a/b.ts", `a\b.ts`} {
		if _, err := SafeJoin(dir, bad); err == nil {
			t.Errorf("SafeJoin accepted %q", bad)
		}
	}
	if _, err := SafeJoin(dir, "seg_0.ts"); err != nil {
		t.Fatalf("SafeJoin rejected valid name: %v", err)
	}
}

func TestCheckUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seg_800_0.ts")
	if err := os.WriteFile(path, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	segs := []manifest.Segment{{ID: 0, FileName: "seg_800_0.ts", Path: path, SizeBytes: fi.Size(), ModTimeUnixNano: fi.ModTime().UnixNano()}}
	if issues := CheckUnchanged(segs); len(issues) != 0 {
		t.Fatalf("issues = %v", issues)
	}
	if err := os.WriteFile(path, []byte("abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	issues := CheckUnchanged(segs)
	if len(issues) != 1 || !strings.Contains(issues[0], "size") {
		t.Fatalf("issues = %v", issues)
	}
}
