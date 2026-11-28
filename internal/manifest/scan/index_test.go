package scan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"archive-lab/internal/manifest"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T, segs ...string) (root, mpath string) {
	t.Helper()
	root = t.TempDir()
	body := "#EXTM3U\n#EXT-X-PLAYLIST-TYPE:VOD\n"
	for i, s := range segs {
		name := "seg" + string(rune('0'+i)) + ".m4s"
		body += "#EXTINF:3.003,\n" + name + "\n"
		writeFile(t, filepath.Join(root, name), s)
	}
	body += "#EXT-X-ENDLIST\n"
	mpath = filepath.Join(root, "index.m3u8")
	writeFile(t, mpath, body)
	return root, mpath
}

func doScan(t *testing.T, root, mpath string, metadata ...string) *Index {
	t.Helper()
	data, err := os.ReadFile(mpath)
	if err != nil {
		t.Fatal(err)
	}
	pl, err := manifest.ParseHLS(mpath, data, manifest.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := Scan(Input{ManifestPath: mpath, Playlist: pl, MediaRoot: root, PlaylistDigest: HashBytes(data), Metadata: metadata})
	if err != nil {
		t.Fatal(err)
	}
	return idx
}

func TestResolveRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	for _, bad := range []string{"../x.m4s", "/etc/passwd", "https://example.com/a.m4s", "a/../../b", ""} {
		if _, err := Resolve(root, bad); err == nil {
			t.Fatalf("Resolve(%q) should fail", bad)
		} else if !strings.Contains(err.Error(), "scan") {
			t.Fatalf("error not located in scan stage: %v", err)
		}
	}
	good, err := Resolve(root, "sub/a.m4s")
	if err != nil || good != "sub/a.m4s" {
		t.Fatalf("Resolve good = %q, %v", good, err)
	}
}

func TestResolveRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.m4s"), "s")
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, err := Resolve(root, "link/secret.m4s"); err == nil {
		t.Fatal("symlink escape should be rejected")
	}
}

func TestScanDetectsSamePathReplacement(t *testing.T) {
	root, mpath := fixture(t, "AAAA", "BBBB")
	idx1 := doScan(t, root, mpath)
	// Same path, different bytes.
	writeFile(t, filepath.Join(root, "seg0.m4s"), "CCCC")
	idx2 := doScan(t, root, mpath)
	if idx1.Digest == idx2.Digest || idx1.InputDigest() == idx2.InputDigest() {
		t.Fatal("replaced segment must change index and input digests")
	}
	if idx1.Files[0].SHA256 == idx2.Files[0].SHA256 {
		t.Fatal("segment digest not updated")
	}
}

func TestScanDeterministic(t *testing.T) {
	root, mpath := fixture(t, "AAAA", "BBBB")
	a := doScan(t, root, mpath)
	b := doScan(t, root, mpath)
	if a.Digest != b.Digest || a.InputDigest() != b.InputDigest() {
		t.Fatal("repeated scans must be byte-identical")
	}
}

func TestScanRejectsOversizedSegmentAndMissingFile(t *testing.T) {
	root, mpath := fixture(t, "AAAA")
	data, _ := os.ReadFile(mpath)
	pl, err := manifest.ParseHLS(mpath, data, manifest.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(Input{ManifestPath: mpath, Playlist: pl, MediaRoot: root, PlaylistDigest: HashBytes(data),
		Limits: Limits{MaxSegmentBytes: 2}}); err == nil {
		t.Fatal("oversized segment must be rejected")
	}
	os.Remove(filepath.Join(root, "seg0.m4s"))
	if _, err := Scan(Input{ManifestPath: mpath, Playlist: pl, MediaRoot: root, PlaylistDigest: HashBytes(data)}); err == nil {
		t.Fatal("missing segment must be rejected")
	}
}

func TestScanRejectsManifestChangedAfterParse(t *testing.T) {
	root, mpath := fixture(t, "AAAA")
	data, _ := os.ReadFile(mpath)
	pl, err := manifest.ParseHLS(mpath, data, manifest.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, mpath, string(data)+"#EXT-X-DISCONTINUITY\n")
	_, err = Scan(Input{ManifestPath: mpath, Playlist: pl, MediaRoot: root, PlaylistDigest: HashBytes(data)})
	if err == nil || !strings.Contains(err.Error(), "changed while scanning") {
		t.Fatalf("expected change detection, got %v", err)
	}
}

func TestLoadDetectsCorruptionAndMissing(t *testing.T) {
	root, mpath := fixture(t, "AAAA")
	idx := doScan(t, root, mpath)
	ipath := filepath.Join(root, "idx.json")
	if err := idx.Save(ipath); err != nil {
		t.Fatal(err)
	}
	got, err := Load(ipath)
	if err != nil || got.Digest != idx.Digest {
		t.Fatalf("Load = %v, %v", got, err)
	}
	// Truncate: no valid index may be returned.
	raw, _ := os.ReadFile(ipath)
	writeFile(t, ipath, string(raw[:len(raw)/2]))
	if _, err := Load(ipath); err == nil {
		t.Fatal("truncated index must be reported as corrupt")
	}
	os.Remove(ipath)
	if _, err := Load(ipath); err != ErrIndexMissing {
		t.Fatalf("missing index should report ErrIndexMissing, got %v", err)
	}
}
