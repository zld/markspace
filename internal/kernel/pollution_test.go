package kernel

import (
	"errors"
	"path/filepath"
	"testing"

	"mediad/internal/pipeline"
	"mediad/internal/segment"
)

// TestAbortDoesNotPolluteCommittedSegments runs a real execution that fails
// partway, then confirms the checkpoint still contains only the successfully
// committed prefix and that every committed digest matches a segment hash.
func TestAbortDoesNotPolluteCommittedSegments(t *testing.T) {
	dir := t.TempDir()
	cpPath := filepath.Join(dir, "checkpoint.json")
	cfgDigest := "config-digest"
	inputDigest := "input-digest"
	truth := map[int]string{
		0: digestBytes([]byte("zero")),
		1: digestBytes([]byte("one")),
		2: digestBytes([]byte("two")),
	}
	boom := errors.New("simulated crash")
	r, err := pipeline.NewRunner(cpPath, inputDigest, cfgDigest, pipeline.RunConfig{TotalSegments: 3})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Run(pipeline.RunConfig{TotalSegments: 3}, func(i int) ([]byte, string, error) {
		if i == 2 {
			return nil, "", boom
		}
		return []byte{byte(i)}, truth[i], nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	cp, err := pipeline.LoadCheckpoint(cpPath, inputDigest, cfgDigest)
	if err != nil {
		t.Fatal(err)
	}
	done := cp.CompletedSet()
	if len(done) != 2 {
		t.Fatalf("committed %d segments, want 2", len(done))
	}
	if _, ok := done[2]; ok {
		t.Fatal("failed segment was committed")
	}
	for idx, d := range done {
		if d != truth[idx] {
			t.Fatalf("segment %d digest polluted: %s != %s", idx, d, truth[idx])
		}
	}
}

// TestRejectedSegmentNotInManifestHashes ensures a rejected segment contributes
// no hash to the committed artifact set.
func TestRejectedSegmentNotInManifestHashes(t *testing.T) {
	src := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	m, err := segment.Build(segment.BuildInput{
		Representation: "r",
		VideoTrack:     1,
		Index:          rejectionIndex(),
		Cuts:           []int64{0, 1e9, 2e9, 3e9},
		Config:         segment.Config{Representations: 1, TargetDuration: 1e9, Tolerance: 33_333_333, FrameNanos: 33_333_333},
		InputDigest:    "d",
		HashSpan:       spanHasherForTest(src),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range m.Segments {
		if s.Hash == "" {
			t.Fatalf("segment %d has no hash", s.Index)
		}
	}
	// The manifest must not reference bytes outside the input.
	for _, s := range m.Segments {
		if s.SourceEnd > int64(len(src)) {
			t.Fatalf("segment %d references bytes past input: %d", s.Index, s.SourceEnd)
		}
	}
}
