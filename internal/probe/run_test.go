package probe

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"streamcheck/internal/manifest"
)

func TestRunContinuesAfterFailures(t *testing.T) {
	good := filepath.Join(t.TempDir(), "good.wav")
	writeWAV(t, good, 4000)
	bad := filepath.Join(t.TempDir(), "bad.ts")
	if err := os.WriteFile(bad, []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := fakeFFprobe(t, `case "$*" in *bad.ts*) echo "bad input" >&2; exit 1;; esac; echo 4.000000`)
	segs := []manifest.Segment{
		{ID: 0, Path: good},
		{ID: 1, Path: bad},
		{ID: 2, Path: good},
	}
	Run(context.Background(), segs, Options{Binary: fake, Timeout: time.Second, Concurrency: 2})
	if segs[0].DurationMS != 4000 || segs[2].DurationMS != 4000 {
		t.Fatalf("good durations = %d, %d", segs[0].DurationMS, segs[2].DurationMS)
	}
	if segs[1].ProbeError == "" {
		t.Fatal("bad segment has no probe error")
	}
}
