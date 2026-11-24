package testkit

import (
	"bytes"
	"testing"

	"archive-recovery/internal/adapter"
	"archive-recovery/internal/index"
	"archive-recovery/internal/job"
	"archive-recovery/internal/media"
)

func TestGenerateDeterministic(t *testing.T) {
	a, err := Generate("av1", ScenarioNormal, 42, 4096)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generate("av1", ScenarioNormal, 42, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Stream, b.Stream) {
		t.Fatal("same seed must reproduce byte-identical stream")
	}
	c, _ := Generate("av1", ScenarioNormal, 43, 4096)
	if bytes.Equal(a.Stream, c.Stream) {
		t.Fatal("different seeds must produce different fixtures")
	}
}

func TestAllCodecsAndScenariosBuild(t *testing.T) {
	for _, codec := range []string{"h264", "h265", "av1"} {
		for _, sc := range []string{ScenarioNormal, ScenarioTruncated, ScenarioBadIndex, ScenarioWrapPTS, ScenarioBoundary} {
			c, err := Generate(codec, sc, 7, 128)
			if err != nil {
				t.Fatalf("%s/%s: %v", codec, sc, err)
			}
			if len(c.Stream) == 0 || len(c.PTS) != c.WantFrames {
				t.Fatalf("%s/%s: empty corpus or PTS mismatch", codec, sc)
			}
		}
	}
}

func TestTruncatedScenarioPropagatesToJob(t *testing.T) {
	c, err := Generate("av1", ScenarioTruncated, 42, 64)
	if err != nil {
		t.Fatal(err)
	}
	r, err := job.Run(job.JobInput{
		Codec: "av1", ChunkBytes: c.ChunkBytes, Seed: c.Seed,
		Stream: c.Stream, PTS: c.PTS, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.TruncatedUnits != c.WantTruncated || r.TruncatedUnits != 1 {
		t.Fatalf("truncation did not propagate: %+v", r)
	}
	if r.FramesTotal != c.WantFrames {
		t.Fatalf("truncated unit must be missing from index: got %d want %d",
			r.FramesTotal, c.WantFrames)
	}
}

func TestBoundaryScenarioKeyframeSpansChunkEdge(t *testing.T) {
	c, err := Generate("av1", ScenarioBoundary, 9, 256)
	if err != nil {
		t.Fatal(err)
	}
	adapt, _ := adapter.Latest("av1")
	sp := adapt.NewSplitter()
	units, _, err := sp.Write(c.Stream)
	if err != nil {
		t.Fatal(err)
	}
	tail, _ := sp.Close()
	units = append(units, tail...)
	var firstKey adapter.Unit
	for _, u := range units {
		if u.IsKeyframe {
			firstKey = u
			break
		}
	}
	edge := firstKey.Offset / c.ChunkBytes
	last := (firstKey.Offset + firstKey.Length - 1) / c.ChunkBytes
	if edge == last {
		t.Fatalf("keyframe must straddle a chunk boundary: off=%d len=%d",
			firstKey.Offset, firstKey.Length)
	}

	// The job must still confirm both spanned chunks and hit every keyframe.
	r, err := job.Run(job.JobInput{
		Codec: "av1", ChunkBytes: c.ChunkBytes, Seed: c.Seed,
		Stream: c.Stream, PTS: c.PTS, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.KeyframeHitPct != 100 || r.RoundtripMatchPct != 100 {
		t.Fatalf("boundary-spanning keyframe broke verification: %+v", r)
	}
}

func TestWrapScenarioExercisesAllRepairReasons(t *testing.T) {
	c, err := Generate("av1", ScenarioWrapPTS, 42, 256)
	if err != nil {
		t.Fatal(err)
	}
	r, err := job.Run(job.JobInput{
		Codec: "av1", ChunkBytes: c.ChunkBytes, Seed: c.Seed,
		Stream: c.Stream, PTS: c.PTS, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.RepairedTimestamps < 5 {
		t.Fatalf("want at least 5 repaired timestamps (missing/dup/regress/wrap), got %d",
			r.RepairedTimestamps)
	}
}

func TestBadIndexScenarioRebuildsRegion(t *testing.T) {
	c, err := Generate("h264", ScenarioBadIndex, 42, 256)
	if err != nil {
		t.Fatal(err)
	}
	li, err := index.ReadLegacyIndex(bytes.NewReader(c.LegacyIndex))
	if err != nil {
		t.Fatal(err)
	}
	if len(li.Entries) < 2 {
		t.Fatal("fixture must contain several legacy entries")
	}
	r, err := job.Run(job.JobInput{
		Codec: "h264", ChunkBytes: c.ChunkBytes, Seed: c.Seed,
		Stream: c.Stream, PTS: c.PTS, LegacyIndex: c.LegacyIndex,
		WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "complete" || r.KeyframeHitPct != 100 {
		t.Fatalf("rebuild after bad index must still complete cleanly: %+v", r)
	}
}

func TestUnknownCodecFailsClassified(t *testing.T) {
	if _, err := Generate("mpeg2", ScenarioNormal, 1, 128); media.ClassOf(err) != media.ClassInvalidArgument {
		t.Fatalf("unknown codec must be invalid-argument, got %v", err)
	}
}
