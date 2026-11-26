package pipeline

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"liveregress/internal/fault"
	"liveregress/internal/fixture"
	"liveregress/internal/manifest"
)

// TestWAVMappingFailureIsMappingFault runs the audio layer through remuxing and
// then the WAV mapping check. A source whose header disagrees with the
// explicit channel map must be a non-recoverable mapping fault (exit 1).
func TestWAVMappingFailureIsMappingFault(t *testing.T) {
	dir := t.TempDir()
	tree, err := fixture.GenerateOne(dir, fixture.CaseBadMapping)
	if err != nil {
		t.Fatal(err)
	}
	outRoot := filepath.Join(tree.Root, ".out")
	r := &Runner{opts: Options{Root: tree.Root, MaxSegmentBytes: 64 << 20}, outRoot: outRoot,
		cp: newTestCheckpoint(t, outRoot)}
	pl, err := manifest.LoadMedia(tree.Root, "a1/index.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	lr := &LayerReport{Layer: "a1", Kind: "audio"}
	for _, seg := range pl.Segments {
		out := r.processSegment("a1", "audio", seg)
		lr.Segments = append(lr.Segments, out.report)
	}
	if len(lr.Segments) == 0 || !lr.Segments[0].Complete {
		os.RemoveAll(outRoot)
		t.Fatalf("expected a committed prefix segment, got %+v", lr.Segments)
	}
	_, cerr := r.verifyWAVMapping(lr, pl)
	if cerr == nil {
		t.Fatal("expected a mapping failure for a 4-channel source")
	}
	if !errors.Is(cerr, fault.ErrMapping) {
		t.Fatalf("want mapping fault, got %v", cerr)
	}
	if fe, _ := fault.As(cerr); fe.Recoverable {
		t.Fatal("invalid mapping must not be recoverable")
	}
}
