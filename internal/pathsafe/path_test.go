package pathsafe_test

import (
	"path/filepath"
	"testing"

	"mediapack/internal/fault"
	"mediapack/internal/pathsafe"
)

func TestJoinAllowsInsideAndRejectsEscape(t *testing.T) {
	root := t.TempDir()
	p, err := pathsafe.Join(root, "index", "chunk.json")
	if err != nil {
		t.Fatal(err)
	}
	if dir := filepath.Dir(p); dir != filepath.Join(root, "index") {
		t.Fatalf("unexpected path %s", p)
	}
	for _, bad := range []string{"../escape", "index/../../escape", "/absolute"} {
		if _, err := pathsafe.Join(root, bad); err == nil {
			t.Fatalf("accepted %q", bad)
		} else if fe, ok := fault.As(err); !ok || fe.Kind != fault.KindPath {
			t.Fatalf("bad error for %q: %v", bad, err)
		}
	}
}
