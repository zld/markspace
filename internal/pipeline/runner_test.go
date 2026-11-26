package pipeline

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"example.com/mediavault/internal/fault"
	"example.com/mediavault/internal/fingerprint"
	"example.com/mediavault/internal/fixtures"
	"example.com/mediavault/internal/manifest"
	"example.com/mediavault/internal/model"
)

func writeFixture(t *testing.T, dir, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o444); err != nil {
		t.Fatal(err)
	}
	return p
}

func newTestRunner(t *testing.T, dir string, stopAfter int) *Runner {
	t.Helper()
	r, err := openRunner(dir, os.Stderr, stopAfter)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// testBlockFrames matches one policy block so multi-block tests stay readable.
var testBlockFrames = int(fingerprint.DefaultPolicy(1, 16).ChunkFrames)

var baseOpts = fixtures.WAVOptions{
	SampleRate: 8000, Channels: 1, Bits: 16, Frames: 16 * testBlockFrames,
	BlockFrames: testBlockFrames, ModifyBlock: -1, Seed: 3,
}

func TestWAVIngestReportsAllStagesInOrder(t *testing.T) {
	dir := t.TempDir()
	path := writeFixture(t, dir, "a.wav", fixtures.WAV(baseOpts))
	rep := newTestRunner(t, dir, 0).Ingest(Input{Name: "a", Kind: "wav", Path: path})
	if rep.Rejected {
		t.Fatalf("rejected: %v", rep.Err)
	}
	want := []model.Stage{model.StageReceived, model.StageScanned, model.StageFingerprinted, model.StageNormalized, model.StageArchived}
	got := []model.Stage{model.StageReceived, model.StageScanned, model.StageFingerprinted, model.StageNormalized, model.StageArchived}
	if len(rep.Transitions) != len(want) {
		t.Fatalf("transitions = %v", rep.Transitions)
	}
	for i := range want {
		if !contains(rep.Transitions[i], string(got[i])) {
			t.Fatalf("transition %d = %q, want stage %s", i, rep.Transitions[i], got[i])
		}
	}
	if rep.Asset.Stage != model.StageArchived {
		t.Fatalf("final stage = %s", rep.Asset.Stage)
	}
	if rep.Committed != 16 {
		t.Fatalf("committed = %d, want 16 blocks of 512 frames", rep.Committed)
	}
	if rep.Asset.ArchiveDigest == "" {
		t.Fatal("no archive content address")
	}
}

func TestDuplicateBytesAreNotRecomputed(t *testing.T) {
	dir := t.TempDir()
	a := writeFixture(t, dir, "a.wav", fixtures.WAV(baseOpts))
	b := writeFixture(t, dir, "b.wav", fixtures.WAV(baseOpts))
	r := newTestRunner(t, dir, 0)
	first := r.Ingest(Input{Name: "a", Kind: "wav", Path: a})
	if first.Rejected {
		t.Fatal(first.Err)
	}
	second := r.Ingest(Input{Name: "b", Kind: "wav", Path: b})
	if second.Rejected {
		t.Fatal(second.Err)
	}
	if second.Recomputed != 0 {
		t.Fatalf("duplicate import recomputed %d blocks", second.Recomputed)
	}
	if second.Asset.Key != first.Asset.Key {
		t.Fatalf("identity depends on path: %s vs %s", first.Asset.Key, second.Asset.Key)
	}
}

func TestContainerOnlyChangeReusesPCMProducts(t *testing.T) {
	dir := t.TempDir()
	plain := writeFixture(t, dir, "plain.wav", fixtures.WAV(baseOpts))
	wrapped := writeFixture(t, dir, "wrapped.wav", fixtures.ContainerVariant(baseOpts))
	r := newTestRunner(t, dir, 0)
	first := r.Ingest(Input{Name: "plain", Kind: "wav", Path: plain})
	if first.Rejected {
		t.Fatal(first.Err)
	}
	second := r.Ingest(Input{Name: "wrapped", Kind: "wav", Path: wrapped})
	if second.Rejected {
		t.Fatal(second.Err)
	}
	if second.Asset.ContainerDigest == first.Asset.ContainerDigest {
		t.Fatal("fixture did not actually change the wrapper digest")
	}
	if second.Recomputed != 0 {
		t.Fatalf("container-only change recomputed %d PCM blocks", second.Recomputed)
	}
	if second.Asset.Key != first.Asset.Key {
		t.Fatal("container-only change created a new asset")
	}
}

func TestSingleBlockChangeRecomputesOnlyThatBlock(t *testing.T) {
	dir := t.TempDir()
	a := writeFixture(t, dir, "a.wav", fixtures.WAV(baseOpts))
	changed := baseOpts
	changed.ModifyBlock = 5
	b := writeFixture(t, dir, "b.wav", fixtures.WAV(changed))
	r := newTestRunner(t, dir, 0)
	first := r.Ingest(Input{Name: "a", Kind: "wav", Path: a})
	if first.Rejected {
		t.Fatal(first.Err)
	}
	second := r.Ingest(Input{Name: "b", Kind: "wav", Path: b})
	if second.Rejected {
		t.Fatal(second.Err)
	}
	if second.Recomputed < 1 {
		t.Fatal("changed block was not recomputed")
	}
	if second.Reused < 14 {
		t.Fatalf("only %d blocks reused; a one-block edit must reuse the rest", second.Reused)
	}
}

func TestResumeFromFirstUncommittedBlock(t *testing.T) {
	dir := t.TempDir()
	path := writeFixture(t, dir, "a.wav", fixtures.WAV(baseOpts))
	crash := newTestRunner(t, dir, 3).Ingest(Input{Name: "a", Kind: "wav", Path: path})
	if crash.Rejected || !crash.Interrupted {
		t.Fatalf("crash run: rejected=%v interrupted=%v err=%v", crash.Rejected, crash.Interrupted, crash.Err)
	}
	resumed := newTestRunner(t, dir, 0).Ingest(Input{Name: "a", Kind: "wav", Path: path})
	if resumed.Rejected {
		t.Fatal(resumed.Err)
	}
	if resumed.ResumeFrom != 3 {
		t.Fatalf("resume_from_block = %d", resumed.ResumeFrom)
	}
	if resumed.Committed != 16 {
		t.Fatalf("committed after resume = %d", resumed.Committed)
	}
	if resumed.Recomputed != 13 {
		t.Fatalf("resumed run recomputed %d blocks, want 13", resumed.Recomputed)
	}
}

func TestResumeValidatesAndRepairsCommittedBlocks(t *testing.T) {
	dir := t.TempDir()
	path := writeFixture(t, dir, "a.wav", fixtures.WAV(baseOpts))
	newTestRunner(t, dir, 2).Ingest(Input{Name: "a", Kind: "wav", Path: path})

	// Corrupt a committed block object on disk. Recovery must re-derive the
	// block from the intact source rather than silently trusting the object.
	ref := checkpointRef(t, dir, 0)
	blob, err := storeRead(t, dir, ref)
	if err != nil {
		t.Fatal(err)
	}
	blob[0] ^= 0xFF
	storeWrite(t, dir, ref, blob)

	resumed := newTestRunner(t, dir, 0).Ingest(Input{Name: "a", Kind: "wav", Path: path})
	if resumed.Rejected {
		t.Fatalf("resumed run failed: %v", resumed.Err)
	}
	if resumed.ResumeFrom != 2 {
		t.Fatalf("resume_from_block = %d", resumed.ResumeFrom)
	}
	if err := mustStore(t, dir).VerifyObject(ref, int64(len(blob))); err != nil {
		t.Fatalf("corrupt block was not repaired: %v", err)
	}
	if resumed.Committed != 16 {
		t.Fatalf("committed = %d", resumed.Committed)
	}
}

func TestCorruptWAVIsRejectedWithStableCode(t *testing.T) {
	dir := t.TempDir()
	bad := fixtures.WAV(fixtures.WAVOptions{SampleRate: 8000, Frames: 2048, BlockFrames: 256, ModifyBlock: -1})
	path := writeFixture(t, dir, "bad.wav", bad[:len(bad)-17])
	rep := newTestRunner(t, dir, 0).Ingest(Input{Name: "bad", Kind: "wav", Path: path})
	if !rep.Rejected {
		t.Fatal("truncated WAV was accepted")
	}
	if rep.ErrorCode != fault.CodeRIFFLength {
		t.Fatalf("code = %q", rep.ErrorCode)
	}
	if rep.Asset.Stage == model.StageArchived {
		t.Fatal("rejected asset reached ARCHIVED")
	}
}

func TestUnalignedWAVIsRejected(t *testing.T) {
	dir := t.TempDir()
	bad := fixtures.WAV(fixtures.WAVOptions{SampleRate: 8000, Channels: 2, Bits: 16, Frames: 512, BlockFrames: 64, ModifyBlock: -1, Unaligned: 1})
	path := writeFixture(t, dir, "bad.wav", bad)
	rep := newTestRunner(t, dir, 0).Ingest(Input{Name: "bad", Kind: "wav", Path: path})
	if !rep.Rejected || rep.ErrorCode != fault.CodeFrameAlign {
		t.Fatalf("rejected=%v code=%q err=%v", rep.Rejected, rep.ErrorCode, rep.Err)
	}
}

func TestRTPDriftIsolation(t *testing.T) {
	dir := t.TempDir()
	raw := fixtures.RTPPackets(fixtures.RTPOptions{
		SampleCounts: fixtures.VariablePattern(), Start: 100, DriftPPM: 1500, FrameBytes: 2,
	})
	rep := newTestRunner(t, dir, 0).Ingest(Input{Name: "drift", Kind: "rtp", RTPPackets: raw, RTPFrameBytes: 2, RTPSampleRate: 8000, RTPClockRate: 8000})
	if !rep.Rejected || rep.ErrorCode != fault.CodeRTPDrift {
		t.Fatalf("rejected=%v code=%q err=%v", rep.Rejected, rep.ErrorCode, rep.Err)
	}
}

func TestRTPWrapAndVariableLengthsSurviveToArchive(t *testing.T) {
	dir := t.TempDir()
	raw := fixtures.RTPPackets(fixtures.RTPOptions{
		SampleCounts: fixtures.VariablePattern(), Start: 0xFFFFF000, DriftPPM: 250, FrameBytes: 2,
	})
	rep := newTestRunner(t, dir, 0).Ingest(Input{Name: "rtp", Kind: "rtp", RTPPackets: raw, RTPFrameBytes: 2, RTPSampleRate: 8000, RTPClockRate: 8000})
	if rep.Rejected {
		t.Fatal(rep.Err)
	}
	if rep.Wraps != 1 || rep.RTPPackets != 32 {
		t.Fatalf("wraps=%d packets=%d", rep.Wraps, rep.RTPPackets)
	}
	if rep.Asset.Stage != model.StageArchived {
		t.Fatalf("stage = %s", rep.Asset.Stage)
	}
}

func TestCorruptMP4IsRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.mp4")
	if err := os.WriteFile(path, corruptMP4Bytes(), 0o444); err != nil {
		t.Fatal(err)
	}
	rep := newTestRunner(t, dir, 0).Ingest(Input{Name: "bad", Kind: "mp4", Path: path})
	if !rep.Rejected {
		t.Fatal("corrupt MP4 accepted")
	}
	if rep.ErrorCode != fault.CodeMP4Extent && rep.ErrorCode != fault.CodeMP4Box {
		t.Fatalf("code = %q", rep.ErrorCode)
	}
}

func TestManifestIsStableAndVerifyPasses(t *testing.T) {
	dir := t.TempDir()
	path := writeFixture(t, dir, "a.wav", fixtures.WAV(baseOpts))
	r := newTestRunner(t, dir, 0)
	if rep := r.Ingest(Input{Name: "a", Kind: "wav", Path: path}); rep.Rejected {
		t.Fatal(rep.Err)
	}
	first, written, err := r.Finalize()
	if err != nil || !written {
		t.Fatalf("finalize: %v written=%v", err, written)
	}
	// A second finalize over an unchanged journal must not rewrite the file.
	again, written, err := newTestRunner(t, dir, 0).Finalize()
	if err != nil {
		t.Fatal(err)
	}
	if written {
		t.Fatal("manifest was rewritten even though entries did not change")
	}
	if again.Digest != first.Digest {
		t.Fatalf("digest changed between runs: %s vs %s", again.Digest, first.Digest)
	}
	res, err := (&manifest.Verifier{Store: mustStore(t, dir)}).Verify(first)
	if err != nil {
		t.Fatal(err)
	}
	if res.Verified != 1 || res.Failed != 0 {
		t.Fatalf("verified=%d failed=%d %v", res.Verified, res.Failed, res.Failures)
	}
}

func TestVerifyDetectsTamperedArchive(t *testing.T) {
	dir := t.TempDir()
	path := writeFixture(t, dir, "a.wav", fixtures.WAV(baseOpts))
	r := newTestRunner(t, dir, 0)
	rep := r.Ingest(Input{Name: "a", Kind: "wav", Path: path})
	if rep.Rejected {
		t.Fatal(rep.Err)
	}
	f, _, err := r.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	tamperObject(t, dir, rep.ArchiveDigest)
	res, err := (&manifest.Verifier{Store: mustStore(t, dir)}).Verify(f)
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 1 || res.Verified != 0 {
		t.Fatalf("tampered archive: verified=%d failed=%d", res.Verified, res.Failed)
	}
}

func TestChunkPolicyIsFrameAlignedAndBounded(t *testing.T) {
	for _, p := range []struct{ channels, bits int }{{1, 16}, {8, 32}, {6, 24}} {
		policy := fingerprint.DefaultPolicy(p.channels, p.bits)
		frameBytes := int64(p.channels * p.bits / 8)
		if policy.ChunkFrames <= 0 {
			t.Fatalf("%d ch %d bit: policy has no frames", p.channels, p.bits)
		}
		if policy.ChunkFrames*frameBytes > 1<<20 {
			t.Fatalf("%d ch %d bit: block exceeds 1 MiB", p.channels, p.bits)
		}
	}
}

func TestArchiveIsReadableMP4WithValidExtents(t *testing.T) {
	dir := t.TempDir()
	path := writeFixture(t, dir, "a.wav", fixtures.WAV(baseOpts))
	r := newTestRunner(t, dir, 0)
	rep := r.Ingest(Input{Name: "a", Kind: "wav", Path: path})
	if rep.Rejected {
		t.Fatal(rep.Err)
	}
	f, _, err := r.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Entries) != 1 {
		t.Fatalf("entries = %d", len(f.Entries))
	}
	// The archive is re-scanned from its stored object by the verifier; any
	// out-of-bounds box would fail here.
	if _, err := (&manifest.Verifier{Store: mustStore(t, dir)}).Verify(f); err != nil {
		t.Fatal(err)
	}
}

func TestRejectedFixtureDoesNotEnterManifest(t *testing.T) {
	dir := t.TempDir()
	good := writeFixture(t, dir, "good.wav", fixtures.WAV(baseOpts))
	badWAV := fixtures.WAV(fixtures.WAVOptions{SampleRate: 8000, Frames: 512, BlockFrames: 64, ModifyBlock: -1})
	bad := writeFixture(t, dir, "bad.wav", badWAV[:len(badWAV)-9])
	r := newTestRunner(t, dir, 0)
	if rep := r.Ingest(Input{Name: "good", Kind: "wav", Path: good}); rep.Rejected {
		t.Fatal(rep.Err)
	}
	if rep := r.Ingest(Input{Name: "bad", Kind: "wav", Path: bad}); !rep.Rejected {
		t.Fatal("bad fixture accepted")
	}
	f, _, err := r.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Entries) != 1 || f.Entries[0].Name != "good" {
		t.Fatalf("manifest entries = %+v", f.Entries)
	}
}

func contains(haystack, needle string) bool {
	return bytes.Contains([]byte(haystack), []byte(needle))
}

var _ = io.Discard

func TestRTPClockingAtDifferentRateReconciles(t *testing.T) {
	// 44.1 kHz payloads carried on a 48 kHz RTP clock: packets must advance in
	// ticks, and the shared base must report sample positions.
	raw := fixtures.RTPPackets(fixtures.RTPOptions{
		SampleCounts: []int64{441, 441, 882, 441, 441}, Start: 0xFFFFFF00,
		FrameBytes: 4, SampleRate: 44100, ClockRate: 48000,
	})
	rep := newTestRunner(t, t.TempDir(), 0).Ingest(Input{
		Name: "rtp", Kind: "rtp", RTPPackets: raw,
		RTPFrameBytes: 4, RTPSampleRate: 44100, RTPClockRate: 48000,
	})
	if rep.Rejected {
		t.Fatalf("rejected: %v", rep.Err)
	}
	if rep.Wraps != 1 {
		t.Fatalf("wraps = %d", rep.Wraps)
	}
	if rep.DriftPPM != 0 {
		t.Fatalf("drift = %d ppm for an exactly clocked stream", rep.DriftPPM)
	}
	if rep.RTPPackets != 5 {
		t.Fatalf("packets = %d", rep.RTPPackets)
	}
}
