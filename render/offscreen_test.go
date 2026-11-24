package render_test

import (
	"bytes"
	"testing"

	"mediacheck/ebml"
	"mediacheck/internal/fixture"
	"mediacheck/render"
	"mediacheck/timeline"
)

func videoSource(t *testing.T, cfg fixture.Config) (render.VideoSource, []byte) {
	t.Helper()
	stream, err := fixture.Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ebml.Scan(bytes.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	var spec *ebml.TrackSpec
	var blocks []*ebml.BlockInfo
	for _, tr := range res.Tracks {
		if tr.IsVideo() {
			spec = tr
		}
	}
	for _, b := range res.Blocks {
		if b.TrackNumber == spec.Number {
			blocks = append(blocks, b)
		}
	}
	return render.VideoSource{
		TrackNumber: spec.Number, Width: int(spec.Width), Height: int(spec.Height),
		Blocks: blocks, TruncatedAt: res.TruncatedAt,
	}, stream
}

func TestExportDeterministicHashes(t *testing.T) {
	src, _ := videoSource(t, fixture.Config{Seed: 17, Frames: 24})
	a, err := render.Export(src, 12)
	if err != nil {
		t.Fatal(err)
	}
	b, err := render.Export(src, 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Frames) != 12 {
		t.Fatalf("want 12 frame records, got %d", len(a.Frames))
	}
	for i := range a.Frames {
		if a.Frames[i].Hash == "" || a.Frames[i].Hash != b.Frames[i].Hash {
			t.Fatalf("hash mismatch or missing at %d: %+v", i, a.Frames[i])
		}
	}
	if a.MissingCount() != 0 {
		t.Fatal("healthy export must have no missing frames")
	}
}

func TestExportSamplesAtFixedTimes(t *testing.T) {
	src, _ := videoSource(t, fixture.Config{Seed: 5, Frames: 24})
	res, err := render.Export(src, 5)
	if err != nil {
		t.Fatal(err)
	}
	// First and last samples coincide with the first and last blocks.
	first := src.Blocks[0].TimeNs
	last := src.Blocks[len(src.Blocks)-1].TimeNs
	if res.Frames[0].SampleNs != first || res.Frames[4].SampleNs != last {
		t.Fatalf("sample endpoints wrong: %d..%d want %d..%d",
			res.Frames[0].SampleNs, res.Frames[4].SampleNs, first, last)
	}
	// Monotonic sample times.
	for i := 1; i < len(res.Frames); i++ {
		if res.Frames[i].SampleNs < res.Frames[i-1].SampleNs {
			t.Fatal("sample times not monotonic")
		}
	}
}

func TestExportTruncationMarkedMissing(t *testing.T) {
	stream, err := fixture.Build(fixture.Config{Seed: 17, Frames: 24})
	if err != nil {
		t.Fatal(err)
	}
	cut, _, err := fixture.TruncateAtCluster(stream, 20)
	if err != nil {
		t.Fatal(err)
	}
	res, scanErr := ebml.Scan(bytes.NewReader(cut))
	if !timeline.IsCategory(scanErr, timeline.CatCorrupt) {
		t.Fatalf("want corrupt scan, got %v", scanErr)
	}
	var spec *ebml.TrackSpec
	for _, tr := range res.Tracks {
		if tr.IsVideo() {
			spec = tr
		}
	}
	var blocks []*ebml.BlockInfo
	for _, b := range res.Blocks {
		if b.TrackNumber == spec.Number {
			blocks = append(blocks, b)
		}
	}
	src := render.VideoSource{
		TrackNumber: spec.Number, Blocks: blocks, TruncatedAt: res.TruncatedAt,
		ExpectedFrames: 24, FrameRate: 24,
	}
	out, err := render.Export(src, 12)
	if err != nil {
		t.Fatal(err)
	}
	if out.MissingCount() == 0 {
		t.Fatal("truncated input must produce missing frame reasons")
	}
	for _, f := range out.Frames {
		if f.Missing && f.Reason == "" {
			t.Fatal("missing frame must carry a reason")
		}
	}
}

func TestExportCRCFailureMarkedMissing(t *testing.T) {
	stream, err := fixture.Build(fixture.Config{Seed: 9, Frames: 16})
	if err != nil {
		t.Fatal(err)
	}
	bad, _, err := fixture.FlipClusterPayload(stream, 0)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ebml.Scan(bytes.NewReader(bad))
	if err != nil {
		t.Fatal(err)
	}
	var spec *ebml.TrackSpec
	for _, tr := range res.Tracks {
		if tr.IsVideo() {
			spec = tr
		}
	}
	var blocks []*ebml.BlockInfo
	for _, b := range res.Blocks {
		if b.TrackNumber == spec.Number {
			blocks = append(blocks, b)
		}
	}
	out, err := render.Export(render.VideoSource{
		TrackNumber: spec.Number, Blocks: blocks, TruncatedAt: res.TruncatedAt,
	}, 8)
	if err != nil {
		t.Fatal(err)
	}
	foundCRC := false
	for _, f := range out.Frames {
		if f.Missing && f.Reason == render.ReasonCheckFailed {
			foundCRC = true
		}
	}
	if !foundCRC {
		t.Fatal("CRC-failed block must surface as a missing frame")
	}
}

func TestExportRejectsBadCount(t *testing.T) {
	src, _ := videoSource(t, fixture.Config{Seed: 1, Frames: 4})
	if _, err := render.Export(src, 0); !timeline.IsCategory(err, timeline.CatConfig) {
		t.Fatalf("want config error, got %v", err)
	}
}
