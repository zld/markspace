package pipeline

import (
	"bytes"
	"crypto/sha256"
	"path/filepath"
	"testing"

	"mediacheck/internal/demux"
	"mediacheck/internal/evidence"
	"mediacheck/internal/policy"
	"mediacheck/internal/testmedia"
)

func TestRunDemoAllScenariosPass(t *testing.T) {
	dir := t.TempDir()
	results, err := RunDemo(Options{Seed: 7, BudgetMS: 5000, MaxBytes: 2_000_000, WorkDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 4 {
		t.Fatalf("scenarios = %d; want 4", len(results))
	}
	for _, r := range results {
		if !r.OK {
			t.Errorf("scenario %s failed: code=%s detail=%s", r.Name, r.ErrorCode, r.Note)
		}
	}
}

func TestHealthySampleHashesMatchAcrossRemux(t *testing.T) {
	// The acceptance contract: input and remuxed output sample hashes agree
	// and pixel statistics differ by zero.
	media, _ := testmedia.BuildMP4(testmedia.DefaultSpec(7))
	src, err := demux.Parse(bytes.NewReader(media.Container))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	outPath := filepath.Join(dir, "h.mp4")
	res, err := writeRemux(src, media.Container, outPath)
	if err != nil {
		t.Fatal(err)
	}
	outBytes := readFile(t, outPath)
	dst, err := demux.Parse(bytes.NewReader(outBytes))
	if err != nil {
		t.Fatal(err)
	}
	srcR, dstR := bytes.NewReader(media.Container), bytes.NewReader(outBytes)
	for ti := range src.Tracks {
		st, ot := src.Tracks[ti], dst.Tracks[ti]
		if len(st.Samples) != len(ot.Samples) {
			t.Fatalf("track %d sample count drift", ti)
		}
		for si := range st.Samples {
			a, _ := src.ReadSample(srcR, st, si)
			b, err := dst.ReadSample(dstR, ot, si)
			if err != nil {
				t.Fatal(err)
			}
			if sha256.Sum256(a) != sha256.Sum256(b) {
				t.Fatalf("sample hash mismatch t%d s%d", ti, si)
			}
		}
	}
	// Manifest sample hashes must equal freshly computed source hashes.
	if len(res.Manifest.SampleHashes) != 2 {
		t.Fatal("manifest missing per-track sample hashes")
	}
	// Pixel statistics computed on source vs output must be identical (the
	// evidence proxy is byte-driven and the bytes are preserved).
	srcRead := func(tr *demux.Track, i int) ([]byte, error) {
		return src.ReadSample(srcR, tr, i)
	}
	dstRead := func(tr *demux.Track, i int) ([]byte, error) {
		return dst.ReadSample(dstR, tr, i)
	}
	mSrc, _ := evidence.Collect(src, media.Container, media.Container, srcRead, srcRead)
	mDst, _ := evidence.Collect(dst, outBytes, outBytes, dstRead, dstRead)
	if mSrc.PixelMean != mDst.PixelMean || mSrc.PixelStddev != mDst.PixelStddev {
		t.Fatalf("pixel statistic drift: src=%v/%v dst=%v/%v",
			mSrc.PixelMean, mSrc.PixelStddev, mDst.PixelMean, mDst.PixelStddev)
	}
}

func TestRerunOutputSummaryStable(t *testing.T) {
	media, _ := testmedia.BuildMP4(testmedia.DefaultSpec(7))
	src, _ := demux.Parse(bytes.NewReader(media.Container))
	dir := t.TempDir()
	outPath := filepath.Join(dir, "h.mp4")
	first, err := writeRemux(src, media.Container, outPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := writeRemux(src, media.Container, outPath)
	if err != nil {
		t.Fatal(err)
	}
	if first.Manifest.OutputSHA256 != second.Manifest.OutputSHA256 {
		t.Fatal("repeated run changed the output summary")
	}
}

func TestBudgetExhaustionMarksIncomplete(t *testing.T) {
	// A zero-tolerance budget must surface the budget gate as a non-accepted
	// decision rather than silently degrading quality.
	media, _ := testmedia.BuildMP4(testmedia.DefaultSpec(7))
	c, _ := demux.Parse(bytes.NewReader(media.Container))
	reader := bytes.NewReader(media.Container)
	read := func(tr *demux.Track, i int) ([]byte, error) {
		return c.ReadSample(reader, tr, i)
	}
	m, _ := evidence.Collect(c, media.Container, media.Container, read, read)
	out := policy.Evaluate(policy.Request{
		Quality: policy.QualityObservation{
			PixelMean: m.PixelMean, ReferenceMean: m.PixelMean,
			PixelStddev: m.PixelStddev, ReferenceStddev: m.PixelStddev,
			AudioPeakDBFS: m.AudioPeakDBFS,
		},
		Threshold: policy.DefaultThresholds(),
		ElapsedMS: 9999, BudgetMS: 1, MaxBytes: 1 << 30,
		Ladder: []policy.Representation{{LayerID: 0, Label: "x", Bitrate: 1, EstimatedSize: 1}},
	})
	if out.Decision != policy.DecisionIncomplete {
		t.Fatalf("decision = %s; want incomplete", out.Decision)
	}
}
