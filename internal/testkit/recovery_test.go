package testkit

import (
	"context"
	"os"
	"testing"

	"mediacore/internal/clock"
	"mediacore/internal/index"
	"mediacore/internal/repair"
	"mediacore/internal/scan"
)

// assertRecoverable checks the stable error classification used for exit 2.
func assertRecoverable(t *testing.T, err error, where string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected error, got nil", where)
	}
	if k := clock.Classify(err); k != clock.KindRecoverable {
		t.Fatalf("%s: want recoverable(2), got kind %d err=%v", where, k, err)
	}
}

func TestTruncationPropagatesToIndexAndStream(t *testing.T) {
	root := t.TempDir()
	fs := generateNormal(t, root)
	// Drop enough mdat bytes to remove the final samples.
	if err := TruncateMP4(fs.MP4, physicalPayload*8+4); err != nil {
		t.Fatal(err)
	}
	// Re-hash sidecar so the digest matches the truncated bytes; the sample
	// table still claims the original count, which must be caught.
	spec := baseSpec(fs)
	if _, err := WriteManifest(spec); err != nil {
		t.Fatal(err)
	}
	_, err := RunPipeline(context.Background(), fs, basePipeline(), "", nil)
	assertRecoverable(t, err, "truncated pipeline")
}

func TestEditListOutOfBounds(t *testing.T) {
	root := t.TempDir()
	fs, err := PrepareDirs(root, "editoob")
	if err != nil {
		t.Fatal(err)
	}
	spec := baseSpec(fs)
	spec.SampleCount = 600 // smaller fixture for the corruption path
	if err := WriteEditOutOfBounds(spec); err != nil {
		t.Fatal(err)
	}
	// Manifest stage must see the resolver failure.
	m, _, err := scan.LoadManifest(fs.MP4, fs.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = scan.OpenFile(fs.MP4, m, scan.DefaultConfig())
	assertRecoverable(t, err, "edit oob scan")
}

func TestTimestampWrapIsRepaired(t *testing.T) {
	root := t.TempDir()
	fs := generateNormal(t, root)
	// Rewrite the RTP sidecar with a wrap near 2^32 at packet 200 and one gap.
	if err := WriteRTPSidecar(fs.RTP, 1, demoTimescale, demoSamples, demoDelta, 200, 500); err != nil {
		t.Fatal(err)
	}
	res, err := RunPipeline(context.Background(), fs, basePipeline(), "", nil)
	if err != nil {
		t.Fatalf("wrap repair failed: %v", err)
	}
	if res.RepairStats.WrapCount == 0 {
		t.Error("wrap not detected")
	}
	if res.RepairStats.GapPackets != 1 {
		t.Errorf("gap packets=%d want 1", res.RepairStats.GapPackets)
	}
	if res.Report.DroppedSamples != 1 {
		t.Errorf("dropped=%d want 1", res.Report.DroppedSamples)
	}
}

func TestBrokenIndexTriggersRebuild(t *testing.T) {
	root := t.TempDir()
	fs := generateNormal(t, root)
	// First run to create the legitimate cache.
	first, err := RunPipeline(context.Background(), fs, basePipeline(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.CacheState != "rebuilt" {
		t.Fatalf("first state=%s", first.CacheState)
	}
	digest := mustDigest(t, fs)
	cachePath := index.CachePath(fs.Index, digest, 1)
	// Corrupt the cache body while keeping its exact length.
	data, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	data[70] ^= 0xff
	if err := os.WriteFile(cachePath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := RunPipeline(context.Background(), fs, basePipeline(), "", nil)
	if err != nil {
		t.Fatalf("rebuild failed: %v", err)
	}
	if second.CacheState != "rebuilt" {
		t.Fatalf("second state=%s want rebuilt", second.CacheState)
	}
	// And the next run trusts it again.
	third, err := RunPipeline(context.Background(), fs, basePipeline(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if third.CacheState != "hit" {
		t.Fatalf("third state=%s want hit", third.CacheState)
	}
}

func TestMissingKeyframeRejected(t *testing.T) {
	root := t.TempDir()
	fs, err := PrepareDirs(root, "nokf")
	if err != nil {
		t.Fatal(err)
	}
	spec := baseSpec(fs)
	spec.SampleCount = 300
	spec.KeyEvery = 30
	// Force the first sync sample off; then seeking anywhere fails.
	spec.ForceNonSync = []uint32{1}
	if err := WriteMP4(spec); err != nil {
		t.Fatal(err)
	}
	m, err := WriteManifest(spec)
	if err != nil {
		t.Fatal(err)
	}
	noSync := ManifestForNoKeyframe(m)
	noSync.ForceNonSync = []uint32{1} // only suppress sample 1 for this check
	f, tracks, err := scan.OpenFile(fs.MP4, noSync, scan.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tr := tracks[0]
	_, err = scan.SeekDecodable(1, 0, tr.Count, tr.SyncNumbers(), func(uint32) clock.Nanos { return 0 })
	assertRecoverable(t, err, "seek before first anchor")
}

func TestIllegalBoxLengthRejected(t *testing.T) {
	root := t.TempDir()
	fs := generateNormal(t, root)
	if err := CorruptBoxLength(fs.MP4); err != nil {
		t.Fatal(err)
	}
	spec := baseSpec(fs)
	if _, err := WriteManifest(spec); err != nil {
		t.Fatal(err)
	}
	m, _, err := scan.LoadManifest(fs.MP4, fs.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = scan.OpenFile(fs.MP4, m, scan.DefaultConfig())
	assertRecoverable(t, err, "illegal box length")
}

func TestInterruptedIndexRerunSafely(t *testing.T) {
	root := t.TempDir()
	fs := generateNormal(t, root)
	// Simulate a temp index left behind by a crashed build; a fresh run must
	// ignore it and still produce a verified canonical cache.
	tmp, err := os.CreateTemp(fs.Index, ".idx-*")
	if err != nil {
		t.Fatal(err)
	}
	tmp.WriteString("partial garbage")
	tmp.Close()
	res, err := RunPipeline(context.Background(), fs, basePipeline(), "", nil)
	if err != nil {
		t.Fatalf("rerun after interruption: %v", err)
	}
	if res.OutputDigest == "" {
		t.Fatal("missing output digest")
	}
	digest := mustDigest(t, fs)
	hdr, err := index.LoadOrVerify(index.CachePath(fs.Index, digest, 1), digest, 1, demoSamples)
	if err != nil {
		t.Fatalf("final cache not verifiable: %v", err)
	}
	if hdr.SampleCount != demoSamples {
		t.Fatalf("cached count=%d", hdr.SampleCount)
	}
}

func TestTruncatedSidecarRecoverable(t *testing.T) {
	// Cross-link: repair must classify its own truncation the same way.
	dir := t.TempDir()
	path := dir + "/p.rtp"
	if err := WriteRTPSidecar(path, 1, 90000, 10, 3000, 0, 0); err != nil {
		t.Fatal(err)
	}
	// Flip the header's packet count above the records actually on disk.
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte{0, 0, 0, 11}, 16); err != nil {
		t.Fatal(err)
	}
	f.Close()
	_, err = repair.RunFile(context.Background(), path, repair.DefaultConfig())
	assertRecoverable(t, err, "truncated rtp sidecar")
}
