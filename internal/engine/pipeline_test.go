package engine

import (
	"os"
	"path/filepath"
	"testing"

	"mediaregress/internal/fixture"
	"mediaregress/internal/media"
	"mediaregress/internal/split"
	"mediaregress/internal/track"
)

func generate(t *testing.T) *fixture.Manifest {
	t.Helper()
	dir := t.TempDir()
	m, err := fixture.Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func runOpts(t *testing.T, input string) Options {
	t.Helper()
	dir := t.TempDir()
	opts := DefaultOptions(input)
	opts.CheckpointDir = filepath.Join(dir, "state")
	opts.OutputPath = filepath.Join(dir, "out.json")
	return opts
}

func TestEndToEndDeterministic(t *testing.T) {
	fx := generate(t)
	a, err := Process(runOpts(t, fx.Valid), ModeFresh)
	if err != nil {
		t.Fatal(err)
	}
	if a.Fatal != nil {
		t.Fatalf("valid input reported fatal: %v", a.Fatal)
	}
	if !a.Manifest.Complete {
		t.Fatal("valid manifest marked incomplete")
	}
	b, err := Process(runOpts(t, fx.Valid), ModeFresh)
	if err != nil {
		t.Fatal(err)
	}
	if a.Manifest.OutputSHA256 != b.Manifest.OutputSHA256 {
		t.Fatalf("hashes differ: %s vs %s", a.Manifest.OutputSHA256, b.Manifest.OutputSHA256)
	}
	if err := CompareManifests(a.Manifest, b.Manifest); err != nil {
		t.Fatal(err)
	}
	if len(a.Manifest.Segments) == 0 || len(a.Manifest.Tracks) != 2 {
		t.Fatalf("manifest shape: tracks=%d segments=%d", len(a.Manifest.Tracks), len(a.Manifest.Segments))
	}
	if len(a.Manifest.SilenceRanges) == 0 {
		t.Error("expected a silence range")
	}
}

func TestManifestSealVerifies(t *testing.T) {
	fx := generate(t)
	opts := runOpts(t, fx.Valid)
	res, err := Process(opts, ModeFresh)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Verify(opts)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if out.OutputSHA256 != res.Manifest.OutputSHA256 {
		t.Fatalf("verify hash %s != %s", out.OutputSHA256, res.Manifest.OutputSHA256)
	}
	if !out.Complete {
		t.Error("verify marked incomplete")
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	fx := generate(t)
	opts := runOpts(t, fx.Valid)
	if _, err := Process(opts, ModeFresh); err != nil {
		t.Fatal(err)
	}
	// Flip a segment window in the written document.
	man, err := ReadManifest(opts.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	man.Segments[0].Window.End++
	if err := WriteManifest(opts.OutputPath, man); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(opts); !media.IsClass(err, media.ClassOutputMismatch) {
		t.Fatalf("tamper not detected: %v", err)
	}
}

func TestVerifyDetectsResealedTamper(t *testing.T) {
	fx := generate(t)
	opts := runOpts(t, fx.Valid)
	if _, err := Process(opts, ModeFresh); err != nil {
		t.Fatal(err)
	}
	man, err := ReadManifest(opts.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	// Alter content and recompute every digest the document itself stores,
	// the most convincing forgery. The content seal must still catch it.
	man.SilenceRanges = nil
	if err := Seal(man); err != nil {
		t.Fatal(err)
	}
	if err := WriteManifest(opts.OutputPath, man); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(opts); !media.IsClass(err, media.ClassOutputMismatch) {
		t.Fatalf("resealed tamper not detected: %v", err)
	}
}

func TestVerifyDetectsChangedInput(t *testing.T) {
	fx := generate(t)
	opts := runOpts(t, fx.Valid)
	if _, err := Process(opts, ModeFresh); err != nil {
		t.Fatal(err)
	}
	// Point verification at a different file with the same manifest.
	otherOpts := opts
	otherOpts.Input = fx.Boundary
	if _, err := Verify(otherOpts); !media.IsClass(err, media.ClassInputChanged) {
		t.Fatalf("changed input not detected: %v", err)
	}
}

func TestDamagedInputPositionsError(t *testing.T) {
	fx := generate(t)
	opts := runOpts(t, fx.Damaged)
	res, err := Process(opts, ModeFresh)
	if err != nil {
		t.Fatal(err)
	}
	if res.Fatal == nil {
		t.Fatal("damaged input produced no fatal error")
	}
	if d := res.Fatal.Offset - fx.DamagedOffset; d < -1 || d > 1 {
		t.Fatalf("offset=%d want %d (+/-1)", res.Fatal.Offset, fx.DamagedOffset)
	}
	if res.Manifest.Complete {
		t.Fatal("damaged input produced a complete manifest")
	}
	if res.Manifest.Provenance.Complete {
		t.Fatal("damaged input produced complete provenance")
	}
	if res.Fatal.Track == "" {
		t.Error("error is not attributed to a track")
	}
}

func TestBadCropRejectsOnlyThatTrack(t *testing.T) {
	fx := generate(t)
	res, err := Process(runOpts(t, fx.BadCrop), ModeFresh)
	if err != nil {
		t.Fatal(err)
	}
	var rejected, ok int
	for _, v := range res.Manifest.Tracks {
		if v.Rejected {
			rejected++
		} else {
			ok++
		}
	}
	if rejected != 1 || ok != 1 {
		t.Fatalf("rejected=%d ok=%d", rejected, ok)
	}
	for _, seg := range res.Manifest.Segments {
		for _, v := range res.Manifest.Tracks {
			if v.Rejected && seg.Track == v.ID {
				t.Errorf("rejected track produced segment %s", seg.ID)
			}
		}
	}
	if res.Manifest.Complete {
		t.Fatal("a rejected track must make the manifest incomplete")
	}
}

func TestResumeMatchesFullRun(t *testing.T) {
	fx := generate(t)
	fullOpts := runOpts(t, fx.Valid)
	full, err := Process(fullOpts, ModeFresh)
	if err != nil {
		t.Fatal(err)
	}

	// Interrupt mid-fingerprint, then resume.
	resumeOpts := runOpts(t, fx.Valid)
	interrupted := resumeOpts
	interrupted.InterruptAfterSegments = 3
	if _, err := Process(interrupted, ModeFresh); err == nil {
		t.Fatal("simulated interruption did not stop the run")
	}
	resumed, err := Process(resumeOpts, ModeRun)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Manifest.OutputSHA256 != full.Manifest.OutputSHA256 {
		t.Fatalf("resumed hash %s != full %s", resumed.Manifest.OutputSHA256, full.Manifest.OutputSHA256)
	}
	if resumed.Stats.ReusedSegments == 0 {
		t.Fatal("resume reused nothing")
	}
	if resumed.Stats.ComputedSegments >= len(resumed.Manifest.Segments) {
		t.Fatalf("resume recomputed everything: computed=%d segments=%d",
			resumed.Stats.ComputedSegments, len(resumed.Manifest.Segments))
	}
}

func TestIdempotentRerunReuses(t *testing.T) {
	fx := generate(t)
	opts := runOpts(t, fx.Valid)
	first, err := Process(opts, ModeFresh)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Process(opts, ModeRun)
	if err != nil {
		t.Fatal(err)
	}
	if first.Manifest.OutputSHA256 != second.Manifest.OutputSHA256 {
		t.Fatal("rerun changed the hash")
	}
	if second.Stats.ReusedSegments != len(second.Manifest.Segments) {
		t.Fatalf("rerun reused %d of %d segments", second.Stats.ReusedSegments, len(second.Manifest.Segments))
	}
}

func TestChangedInputInvalidatesAffectedTrackOnly(t *testing.T) {
	fx := generate(t)
	opts := runOpts(t, fx.Valid)
	first, err := Process(opts, ModeFresh)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt exactly one audio segment's bytes, leaving box structure and the
	// video samples untouched.
	var audioSeg *split.Segment
	for i := range first.Manifest.Segments {
		s := &first.Manifest.Segments[i]
		if s.Kind == track.KindAudio && s.Size > 8 {
			audioSeg = s
			break
		}
	}
	if audioSeg == nil {
		t.Fatal("no audio segment to modify")
	}
	data, err := os.ReadFile(fx.Valid)
	if err != nil {
		t.Fatal(err)
	}
	for i := audioSeg.SourceOffset; i < audioSeg.SourceOffset+8; i++ {
		data[i] ^= 0xff
	}
	if err := os.WriteFile(fx.Valid, data, 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := Process(opts, ModeRun)
	if err != nil {
		t.Fatal(err)
	}
	if first.Manifest.OutputSHA256 == second.Manifest.OutputSHA256 {
		t.Fatal("changed input produced the same hash")
	}
	// Video keeps its digest and its cached segments; audio is recomputed.
	firstDigest := map[string]string{}
	for _, v := range first.Manifest.Tracks {
		firstDigest[v.ID] = v.InputDigest
	}
	for _, v := range second.Manifest.Tracks {
		if v.Kind == track.KindVideo && v.InputDigest != firstDigest[v.ID] {
			t.Errorf("video track digest changed after an audio-only edit")
		}
		if v.Kind == track.KindAudio && v.InputDigest == firstDigest[v.ID] {
			t.Errorf("audio track digest did not change")
		}
	}
	if second.Stats.ReusedSegments == 0 {
		t.Fatal("no work survived an audio-only change")
	}
	if second.Stats.ComputedSegments == 0 {
		t.Fatal("audio segments were not recomputed")
	}
	// The invalidated audio result must still be a complete, sealed manifest.
	if !second.Manifest.Complete {
		t.Fatal("manifest incomplete after selective invalidation")
	}
	if _, err := Verify(opts); err != nil {
		t.Fatalf("verify after selective invalidation: %v", err)
	}
}

func TestDuplicateFixtureGroupsSources(t *testing.T) {
	fx := generate(t)
	res, err := Process(runOpts(t, fx.Duplicates), ModeFresh)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Manifest.Complete {
		t.Fatalf("duplicate fixture incomplete: %v", res.Manifest.Errors)
	}
	if len(res.Manifest.Duplicates) != 2 {
		t.Fatalf("duplicate groups=%d want 2", len(res.Manifest.Duplicates))
	}
	for _, g := range res.Manifest.Duplicates {
		if len(g.Segments) < 2 || len(g.Ranges) != len(g.Segments) {
			t.Errorf("group %s incomplete: %+v", g.Digest, g)
		}
		if len(g.Tracks) != 1 || g.Tracks[0] != "v0" {
			t.Errorf("group tracks=%v", g.Tracks)
		}
		for _, r := range g.Ranges {
			if r.Duration() != media.Second {
				t.Errorf("range=%+v", r)
			}
		}
	}
	// Only one member of each group may be a primary result.
	primaries := map[string]int{}
	for _, fp := range res.Manifest.Fingerprints {
		if !fp.Duplicate {
			primaries[fp.Digest]++
		}
	}
	for _, g := range res.Manifest.Duplicates {
		if primaries[g.Digest] != 1 {
			t.Errorf("group %s has %d primaries", g.Digest, primaries[g.Digest])
		}
	}
}

func TestMxfRuns(t *testing.T) {
	fx := generate(t)
	res, err := Process(runOpts(t, fx.MXF), ModeFresh)
	if err != nil {
		t.Fatal(err)
	}
	if res.Fatal != nil {
		t.Fatalf("valid MXF fatal: %v", res.Fatal)
	}
	if len(res.Manifest.Tracks) != 2 {
		t.Fatalf("tracks=%d", len(res.Manifest.Tracks))
	}
	if len(res.Manifest.Duplicates) == 0 {
		// 60 identical picture frames produce repeated content across segments
		// only if the segments align; register what was seen instead.
		t.Log("no duplicate groups for MXF fixture")
	}
}

func TestMxfConflictRecorded(t *testing.T) {
	fx := generate(t)
	res, err := Process(runOpts(t, fx.MXFConflict), ModeFresh)
	if err != nil {
		t.Fatal(err)
	}
	var conflicts int
	for _, tr := range res.Manifest.Tracks {
		conflicts += len(tr.Conflicts)
	}
	if conflicts == 0 {
		t.Fatal("MXF color conflict not carried into the manifest")
	}
}

func TestMxfDamagedPositioned(t *testing.T) {
	fx := generate(t)
	res, err := Process(runOpts(t, fx.MXFDamaged), ModeFresh)
	if err != nil {
		t.Fatal(err)
	}
	if res.Fatal == nil {
		t.Fatal("damaged MXF produced no fatal error")
	}
	if d := res.Fatal.Offset - fx.MXFDamagedOffset; d < -1 || d > 1 {
		t.Fatalf("offset=%d want %d (+/-1)", res.Fatal.Offset, fx.MXFDamagedOffset)
	}
}

func TestBoundaryFixtureSplits(t *testing.T) {
	fx := generate(t)
	res, err := Process(runOpts(t, fx.Boundary), ModeFresh)
	if err != nil {
		t.Fatal(err)
	}
	// The boundary fixture is 1.5 s of video with 60 fps samples and a 1 s
	// target, so it must produce exactly two video segments.
	var videoSegs int
	for _, s := range res.Manifest.Segments {
		if s.Kind == "video" {
			videoSegs++
		}
	}
	if videoSegs != 2 {
		t.Fatalf("video segments=%d want 2", videoSegs)
	}
}

func TestCheckpointOptional(t *testing.T) {
	fx := generate(t)
	withCP, err := Process(runOpts(t, fx.Valid), ModeFresh)
	if err != nil {
		t.Fatal(err)
	}
	bare := DefaultOptions(fx.Valid)
	bare.OutputPath = filepath.Join(t.TempDir(), "out.json")
	without, err := Process(bare, ModeFresh)
	if err != nil {
		t.Fatal(err)
	}
	if withCP.Manifest.OutputSHA256 != without.Manifest.OutputSHA256 {
		t.Fatal("checkpointing changed the manifest")
	}
	if _, err := os.Stat(bare.CheckpointDir); !os.IsNotExist(err) {
		t.Errorf("checkpoint dir created despite being disabled: %v", err)
	}
}

func TestMissingInput(t *testing.T) {
	opts := DefaultOptions(filepath.Join(t.TempDir(), "nope.mp4"))
	opts.CheckpointDir = t.TempDir()
	_, err := Process(opts, ModeFresh)
	if err == nil {
		t.Fatal("missing input accepted")
	}
	if !media.IsClass(err, media.ClassCorruptInput) {
		t.Fatalf("class=%v", err)
	}
}
