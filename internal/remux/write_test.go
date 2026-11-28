package remux

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"mediacheck/internal/demux"
	merr "mediacheck/internal/errors"
	"mediacheck/internal/testmedia"
)

func buildParsed(t *testing.T, seed uint64) ([]byte, *demux.Container) {
	t.Helper()
	media, err := testmedia.BuildMP4(testmedia.DefaultSpec(seed))
	if err != nil {
		t.Fatal(err)
	}
	c, err := demux.Parse(bytes.NewReader(media.Container))
	if err != nil {
		t.Fatal(err)
	}
	return media.Container, c
}

func TestRemuxPreservesEverySampleByte(t *testing.T) {
	src, c := buildParsed(t, 7)
	dir := t.TempDir()
	out := filepath.Join(dir, "out.mp4")
	res, err := Write(bytes.NewReader(src), c, HashBytes(src), out)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Rebuilt {
		t.Fatal("first write must report a rebuild")
	}
	// Re-parse the output and confirm every sample hashes equal the source.
	out2, err := demux.Parse(bytes.NewReader(mustRead(t, out)))
	if err != nil {
		t.Fatal(err)
	}
	srcReader := bytes.NewReader(src)
	outReader := bytes.NewReader(mustRead(t, out))
	if len(out2.Tracks) != len(c.Tracks) {
		t.Fatalf("track count changed: %d -> %d", len(c.Tracks), len(out2.Tracks))
	}
	for ti := range c.Tracks {
		st, ot := c.Tracks[ti], out2.Tracks[ti]
		if len(st.Samples) != len(ot.Samples) {
			t.Fatalf("track %d sample count changed", ti)
		}
		for si := range st.Samples {
			a, err := c.ReadSample(srcReader, st, si)
			if err != nil {
				t.Fatal(err)
			}
			b, err := out2.ReadSample(outReader, ot, si)
			if err != nil {
				t.Fatal(err)
			}
			if sha256.Sum256(a) != sha256.Sum256(b) {
				t.Fatalf("sample t%d s%d changed during remux", ti, si)
			}
		}
	}
}

func TestRerunIsIdempotentAndStable(t *testing.T) {
	src, c := buildParsed(t, 7)
	dir := t.TempDir()
	out := filepath.Join(dir, "out.mp4")
	first, err := Write(bytes.NewReader(src), c, HashBytes(src), out)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Write(bytes.NewReader(src), c, HashBytes(src), out)
	if err != nil {
		t.Fatal(err)
	}
	if second.Rebuilt {
		t.Fatal("identical input rerun must reuse the verified output")
	}
	if first.Manifest.OutputSHA256 != second.Manifest.OutputSHA256 {
		t.Fatal("output summary must be stable across runs")
	}
	if first.Manifest.InputSHA256 != second.Manifest.InputSHA256 {
		t.Fatal("input summary must be stable across runs")
	}
}

func TestCorruptArtifactIsSafelyRebuilt(t *testing.T) {
	src, c := buildParsed(t, 7)
	dir := t.TempDir()
	out := filepath.Join(dir, "out.mp4")
	first, err := Write(bytes.NewReader(src), c, HashBytes(src), out)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the container while the completion marker stays in place.
	raw := mustRead(t, out)
	raw[len(raw)-1] ^= 0xff
	if err := os.WriteFile(out, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := Write(bytes.NewReader(src), c, HashBytes(src), out)
	if err != nil {
		t.Fatalf("rebuild after corruption failed: %v", err)
	}
	if !second.Rebuilt {
		t.Fatal("corrupt artifact must trigger a rebuild")
	}
	if second.Manifest.OutputSHA256 != first.Manifest.OutputSHA256 {
		t.Fatal("rebuilt output must reproduce the original summary")
	}
	// The freshly rebuilt file must now verify again.
	if _, err := demux.Parse(bytes.NewReader(mustRead(t, out))); err != nil {
		t.Fatalf("rebuilt container does not parse: %v", err)
	}
}

func TestChangedInputRebuilds(t *testing.T) {
	src, c := buildParsed(t, 7)
	dir := t.TempDir()
	out := filepath.Join(dir, "out.mp4")
	if _, err := Write(bytes.NewReader(src), c, HashBytes(src), out); err != nil {
		t.Fatal(err)
	}
	src2, c2 := buildParsed(t, 8)
	res, err := Write(bytes.NewReader(src2), c2, HashBytes(src2), out)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Rebuilt {
		t.Fatal("a different input summary must force a rebuild")
	}
}

func TestNoTempFilesLeftBehind(t *testing.T) {
	src, c := buildParsed(t, 7)
	dir := t.TempDir()
	out := filepath.Join(dir, "out.mp4")
	if _, err := Write(bytes.NewReader(src), c, HashBytes(src), out); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" || hasPrefix(e.Name(), ".remux-") {
			t.Fatalf("temporary file leaked: %s", e.Name())
		}
	}
}

func TestManifestTracksSampleCounts(t *testing.T) {
	src, c := buildParsed(t, 7)
	dir := t.TempDir()
	res, err := Write(bytes.NewReader(src), c, HashBytes(src), filepath.Join(dir, "o.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.TrackCount != 2 ||
		len(res.Manifest.SampleHashes[0]) != 48 ||
		len(res.Manifest.SampleHashes[1]) != 96 {
		t.Fatalf("manifest sample accounting wrong: %+v", res.Manifest)
	}
	if _, ok := merr.As(nil); ok {
		t.Fatal("nil error sanity")
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
