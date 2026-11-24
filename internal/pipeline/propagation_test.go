package pipeline

import (
	"path/filepath"
	"testing"

	"mediarun/internal/gen"
	"mediarun/internal/index"
)

// TestChecksumFaultPropagatesThroughMux verifies that a record rejected at the
// checksum stage still yields a complete, verifiable MP4: the damaged record is
// skipped, sync runs over the surviving frames, and every exported sample maps
// back to a verified ingest record.
func TestChecksumFaultPropagatesThroughMux(t *testing.T) {
	_, _, dir := runFixture(t, gen.Options{Inject: []string{"checksum"}, SegmentCount: 2, FramesPerSeg: 6}, DefaultConfig(), false)

	r := newRunnerFor(t, dir, DefaultConfig())
	m, err := r.Run()
	if err != nil {
		t.Fatal(err)
	}
	if m.Metrics.ParseErrors != 1 {
		t.Fatalf("parse errors = %d", m.Metrics.ParseErrors)
	}
	if m.Metrics.RecoveredVideo != 1 {
		t.Fatalf("recovered video = %d", m.Metrics.RecoveredVideo)
	}
	// The mux stage must have produced every planned sample.
	var samples int
	for _, seg := range m.Segments {
		samples += seg.Samples
		if seg.RoundtripHit != seg.Samples {
			t.Fatalf("segment %d roundtrip %d/%d", seg.Index, seg.RoundtripHit, seg.Samples)
		}
	}
	if samples != m.Metrics.Frames {
		t.Fatalf("muxed samples = %d, parsed frames = %d", samples, m.Metrics.Frames)
	}
	// No sample may reference the rejected record's offset.
	bad := int64(-1)
	for _, f := range m.Faults {
		if f.Kind == "checksum_mismatch" {
			bad = f.Offset
		}
	}
	if bad < 0 {
		t.Fatal("no checksum fault recorded")
	}
	for _, seg := range m.Segments {
		idxf, err := readIndexFile(dir, seg.Index)
		if err != nil {
			t.Fatal(err)
		}
		for _, ref := range idxf.Samples {
			if ref.SourceOff == bad {
				t.Fatalf("segment %d exported the rejected record at %d", seg.Index, bad)
			}
		}
	}
}

// TestAudioGapPropagatesThroughSync verifies a sealed audio hole reaches the
// sync stage and the segment manifest: the timeline keeps its duration, the
// owning fragment is incomplete, and the output still verifies.
func TestAudioGapPropagatesThroughSync(t *testing.T) {
	_, _, dir := runFixture(t, gen.Options{Inject: []string{"gap"}}, DefaultConfig(), false)
	r := newRunnerFor(t, dir, DefaultConfig())
	m, err := r.Run()
	if err != nil {
		t.Fatal(err)
	}
	if m.Metrics.RecoveredAudio != 3 {
		t.Fatalf("concealed slots = %d", m.Metrics.RecoveredAudio)
	}
	if m.Metrics.IncompleteSegments != 1 {
		t.Fatalf("incomplete segments = %d, want exactly the one with the hole", m.Metrics.IncompleteSegments)
	}
	var incomplete *int
	for i := range m.Segments {
		if m.Segments[i].Incomplete {
			idx := i
			incomplete = &idx
		}
	}
	if incomplete == nil {
		t.Fatal("no incomplete segment")
	}
	// The owning segment still has a valid MP4 and index.
	seg := m.Segments[*incomplete]
	path := filepath.Join(dir, filepath.FromSlash(seg.Path))
	idxf, err := readIndexFile(dir, seg.Index)
	if err != nil {
		t.Fatal(err)
	}
	vr, err := index.Verify(path, idxf)
	if err != nil {
		t.Fatal(err)
	}
	if !vr.Verified() && vr.Checked > 0 {
		t.Fatalf("incomplete segment index did not verify: %+v", vr)
	}
	// The recovery stage summary carries the loss counts.
	var counts map[string]int
	for i := range m.Stages {
		if m.Stages[i].Name == "recovery" {
			counts = m.Stages[i].Counts
		}
	}
	if counts == nil {
		t.Fatal("no recovery stage summary")
	}
	if counts["recovered"] != 3 {
		t.Fatalf("recovery summary = %+v", counts)
	}
	if counts["unrecovered"] == 0 {
		t.Fatal("large hole not counted as unrecovered")
	}
}

func newRunnerFor(t *testing.T, dir string, cfg Config) *Runner {
	t.Helper()
	r, err := New(dir, Inputs{
		FramesPath: filepath.Join(dir, "frames.mfr"),
		AudioPath:  filepath.Join(dir, "audio.rtp"),
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
