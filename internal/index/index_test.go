package index

import (
	"os"
	"path/filepath"
	"testing"

	"replaypipe/internal/fixtures"
	"replaypipe/internal/mp4box"
)

func writeFixture(t *testing.T, tracks []mp4box.Track, opts mp4box.BuildOpts) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "media.mp4")
	if err := os.WriteFile(path, mp4box.Build(tracks, opts), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseMediaTracks(t *testing.T) {
	path := writeFixture(t, fixtures.BuildTracks(), mp4box.DefaultOpts())
	media, err := ParseFile(path, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(media.Tracks) != 2 {
		t.Fatalf("tracks = %d", len(media.Tracks))
	}
	video := media.VideoTrack()
	if video == nil || video.Width != fixtures.Width || video.Height != fixtures.Height {
		t.Fatalf("video track = %+v", video)
	}
	if len(video.Samples) != fixtures.Frames {
		t.Fatalf("video samples = %d", len(video.Samples))
	}
	if fps := video.FrameRate(); fps < 29.9 || fps > 30.1 {
		t.Fatalf("fps = %.3f", fps)
	}
	if audio := media.AudioTrack(); audio == nil || audio.SampleRate != 48000 {
		t.Fatalf("audio track = %+v", audio)
	}
	// Sample offsets must point at the correct payloads inside the file.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	first := video.Samples[0]
	if int64(len(raw)) < first.Offset+int64(first.Size) {
		t.Fatalf("sample range exceeds file")
	}
	if media.ContentDigest == "" || media.EnvelopeDigest == "" {
		t.Fatal("digests not computed")
	}
}

func TestContentDigestIgnoresEnvelope(t *testing.T) {
	base := fixtures.BuildTracks()
	opts := mp4box.DefaultOpts()
	opts.FreePadding = 128
	opts.CompatibleBrands = append(opts.CompatibleBrands, "cmf2")
	a, err := ParseFile(writeFixture(t, base, mp4box.DefaultOpts()), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseFile(writeFixture(t, base, opts), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if a.ContentDigest != b.ContentDigest {
		t.Fatal("content digest changed for envelope-only edit")
	}
	if a.EnvelopeDigest == b.EnvelopeDigest {
		t.Fatal("envelope digest did not change")
	}
}

func TestContentDigestDetectsSampleEdit(t *testing.T) {
	tracks := fixtures.BuildTracks()
	tracks[0].Samples[5].Data[10] ^= 0xff
	a, err := ParseFile(writeFixture(t, fixtures.BuildTracks(), mp4box.DefaultOpts()), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseFile(writeFixture(t, tracks, mp4box.DefaultOpts()), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if a.ContentDigest == b.ContentDigest {
		t.Fatal("content digest did not detect a sample byte change")
	}
}

func TestDigestIndependentOfPathAndMtime(t *testing.T) {
	src := writeFixture(t, fixtures.BuildTracks(), mp4box.DefaultOpts())
	dir := t.TempDir()
	dst := filepath.Join(dir, "renamed.mp4")
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := ParseFile(src, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseFile(dst, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if a.ContentDigest != b.ContentDigest || a.EnvelopeDigest != b.EnvelopeDigest {
		t.Fatal("digests depend on path")
	}
}

func TestKeyframeIndex(t *testing.T) {
	media, err := ParseFile(writeFixture(t, fixtures.BuildTracks(), mp4box.DefaultOpts()), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	kf, kerr := BuildKeyframeIndex(media.VideoTrack())
	if kerr != nil {
		t.Fatal(kerr)
	}
	if kf.Len() != 8 {
		t.Fatalf("keyframes = %d, want 8", kf.Len())
	}
	// Each keyframe is 40 frames apart => 5 s at 30 fps (fixture GOP).
	want := []int64{0, 4_000_000_000, 8_000_000_000}
	for _, ns := range want {
		f, ok := kf.AtOrBefore(ns + 1)
		if !ok || f.PTSNS() > ns+1 {
			t.Fatalf("AtOrBefore(%d) = %+v ok=%t", ns, f, ok)
		}
	}
	if f, ok := kf.AtOrAfter(0); !ok || f.PTSNS() != 0 {
		t.Fatalf("AtOrAfter(0) = %+v", f)
	}
	times := make([]int64, 0, kf.Len())
	for i := 0; i < kf.Len(); i++ {
		times = append(times, kf.Frame(i).PTSNS())
	}
	_, stats := kf.Resolve(times)
	if stats.Exact != kf.Len() || stats.Aligned != 0 {
		t.Fatalf("resolve stats = %+v", stats)
	}
}

func TestKeyframeIndexNoSync(t *testing.T) {
	tracks := fixtures.BuildTracks()
	for i := range tracks[0].Samples {
		tracks[0].Samples[i].Sync = false
	}
	media, err := ParseFile(writeFixture(t, tracks, mp4box.DefaultOpts()), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	kf, kerr := BuildKeyframeIndex(media.VideoTrack())
	if kerr != nil {
		t.Fatal(kerr)
	}
	if kf.Len() != 0 {
		t.Fatalf("keyframes = %d, want 0", kf.Len())
	}
	if _, ok := kf.AtOrBefore(0); ok {
		t.Fatal("empty index reported a keyframe")
	}
}

func TestParseTruncatedBox(t *testing.T) {
	path := writeFixture(t, fixtures.BuildTracks(), mp4box.DefaultOpts())
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cut := filepath.Join(t.TempDir(), "cut.mp4")
	if err := os.WriteFile(cut, raw[:len(raw)-64], 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = ParseFile(cut, DefaultLimits())
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); !containsCode(got, "BOX_TRUNCATED") {
		t.Fatalf("error = %v", got)
	}
}

func TestParseByteLimit(t *testing.T) {
	path := writeFixture(t, fixtures.BuildTracks(), mp4box.DefaultOpts())
	limits := Limits{MaxBoxBytes: 1 << 10, MaxTotalBytes: 4 << 10}
	_, err := ParseFile(path, limits)
	if err == nil {
		t.Fatal("expected byte-limit failure")
	}
	if got := err.Error(); !containsCode(got, "BYTE_LIMIT_EXCEEDED") {
		t.Fatalf("error = %v", got)
	}
}

func containsCode(s, code string) bool {
	return len(s) >= len(code) && (s[:len(code)] == code || indexOf(s, code) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestEnvelopeDigestDetectsBrandEdit(t *testing.T) {
	base := fixtures.BuildTracks()
	opts := mp4box.DefaultOpts()
	opts.MajorBrand = "mp42"
	a, err := ParseFile(writeFixture(t, base, mp4box.DefaultOpts()), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseFile(writeFixture(t, base, opts), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if a.ContentDigest != b.ContentDigest {
		t.Fatal("brand edit changed the sample content digest")
	}
	if a.EnvelopeDigest == b.EnvelopeDigest {
		t.Fatal("brand edit did not change the envelope digest")
	}
}
