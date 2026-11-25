package manifest

import (
	"testing"

	"github.com/zld/media-regress/internal/error"
)

func TestBuildAndVerifyValid(t *testing.T) {
	m := sampleManifest(t)
	errs := Verify(m, sampleTarget(), true)
	if len(errs) != 0 {
		t.Fatalf("unexpected verification errors: %v", errs)
	}
	if m.Digest == "" || len(m.Track.Segments) != 12 {
		t.Fatalf("manifest not finalized: %+v", m)
	}
	if m.Summary.ManifestSegments != 12 || m.Summary.Stages != 5 {
		t.Fatalf("summary %+v", m.Summary)
	}
	if len(m.Representations) != 1 || m.Representations[0].Render.Rotation != 90 {
		t.Fatalf("representations %+v", m.Representations)
	}
}

func TestDigestDeterministic(t *testing.T) {
	a := sampleManifest(t)
	b := sampleManifest(t)
	if a.Digest != b.Digest {
		t.Fatalf("digest not deterministic:\n%s\n%s", a.Digest, b.Digest)
	}
}

func TestDigestChangesWithSegmentOrder(t *testing.T) {
	a := sampleManifest(t)
	b := sampleManifest(t)
	// Reordering segments must not change the digest because canonicalization
	// sorts by index.
	b.Track.Segments[0], b.Track.Segments[1] = b.Track.Segments[1], b.Track.Segments[0]
	_ = b.Finalize()
	if a.Digest != b.Digest {
		t.Fatalf("canonical digest changed under reordering")
	}
	// But changing a digest must change it.
	b.Track.Segments[0].Digest = "tampered"
	_ = b.Finalize()
	if a.Digest == b.Digest {
		t.Fatal("changing a segment digest must change the manifest digest")
	}
}

func TestVerifyDetectsGap(t *testing.T) {
	b := NewBuilder("valid.mp4", FormatMP4, "d")
	b.SetAudioTarget(sampleTarget())
	_ = b.AddTrack(videoTrack())
	_ = b.AddTrack(audioTrack())
	for i := 1; i <= 4; i++ {
		start := int64(i-1) * 15000
		if i == 3 {
			start += 1000 // gap
		}
		b.AddSegment(Segment{Index: i, TrackID: 2, StartTick: start, EndTick: start + 15000,
			Frames: 12000, Digest: "x", Bytes: 1})
	}
	_ = b.Finish(Summary{})
	errs := Verify(b.Manifest(), sampleTarget(), false)
	if !hasKind(errs, merr.KindContinuity) {
		t.Fatalf("gap not reported: %v", errs)
	}
}

func TestVerifyDetectsMissingSegmentNumbering(t *testing.T) {
	b := NewBuilder("valid.mp4", FormatMP4, "d")
	b.SetAudioTarget(sampleTarget())
	_ = b.AddTrack(videoTrack())
	_ = b.AddTrack(audioTrack())
	for i := 1; i <= 4; i++ {
		if i == 3 {
			continue
		}
		start := int64(i-1) * 15000
		b.AddSegment(Segment{Index: i, TrackID: 2, StartTick: start, EndTick: start + 15000,
			Frames: 12000, Digest: "x", Bytes: 1})
	}
	_ = b.Finish(Summary{})
	if errs := Verify(b.Manifest(), sampleTarget(), false); len(errs) == 0 {
		t.Fatal("dropped segment must be reported")
	}
}

func TestVerifyDetectsAudioTargetMismatch(t *testing.T) {
	m := sampleManifest(t)
	wrong := sampleTarget()
	wrong.SampleRate = 44100
	errs := Verify(m, wrong, false)
	if !hasKind(errs, merr.KindInconsistent) {
		t.Fatalf("mismatch not reported: %v", errs)
	}
	wrong = sampleTarget()
	wrong.Layout = "5.1"
	if errs := Verify(m, wrong, false); len(errs) == 0 {
		t.Fatal("layout mismatch must be reported")
	}
	wrong = sampleTarget()
	wrong.LoudnessLUFS = "-23"
	if errs := Verify(m, wrong, false); len(errs) == 0 {
		t.Fatal("loudness mismatch must be reported")
	}
}

func TestVerifyDetectsTamperedDigest(t *testing.T) {
	m := sampleManifest(t)
	m.Track.Segments[4].Digest = "changed"
	errs := Verify(m, sampleTarget(), true)
	if !hasKind(errs, merr.KindDigest) {
		t.Fatalf("tampered digest not reported: %v", errs)
	}
}

func TestVerifyDetectsEmptyWindow(t *testing.T) {
	m := sampleManifest(t)
	m.Representations[0].EndTick = m.Representations[0].StartTick
	if errs := Verify(m, sampleTarget(), false); len(errs) == 0 {
		t.Fatal("empty representation window must be reported")
	}
}

func hasKind(errs []error, kind merr.Kind) bool {
	for _, err := range errs {
		if e, ok := merr.As(err); ok && e.Kind == kind {
			return true
		}
	}
	return false
}
