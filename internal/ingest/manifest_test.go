package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"medialab/internal/model"
)

type sampleFile struct {
	rel  string
	data []byte
}

func writeSampleRoot(t *testing.T, files []sampleFile) string {
	t.Helper()
	root := t.TempDir()
	for _, f := range files {
		p := filepath.Join(root, f.rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, f.data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func hashOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func frameFor(rel string, track model.TrackID, seq uint32, pts int64, data []byte) model.Frame {
	return model.Frame{
		Track: track, Sequence: seq, PTS: model.Timestamp(pts),
		Width: 64, Height: 48, Size: len(data),
		Color: model.Color{Primaries: "bt709", Transfer: "bt709", Range: "full"},
		Path:  rel, SHA256: hashOf(data),
	}
}

func twoTrackRoot(t *testing.T) (string, Manifest) {
	files := []sampleFile{
		{"a/one.bin", []byte("alpha-1")},
		{"a/two.bin", []byte("alpha-2")},
		{"b/one.bin", []byte("beta-1")},
	}
	root := writeSampleRoot(t, files)
	m := Manifest{Version: 1, EpochNS: 0, Frames: []model.Frame{
		frameFor("a/two.bin", 1, 2, 40, files[1].data),
		frameFor("b/one.bin", 2, 1, 20, files[2].data),
		frameFor("a/one.bin", 1, 1, 20, files[0].data),
	}}
	return root, m
}

func TestValidateSortsAndRebases(t *testing.T) {
	root, m := twoTrackRoot(t)
	res, warns, err := Validate(m, LoadOptions{RootDir: root, VerifyContent: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	want := []struct {
		track model.TrackID
		seq   uint32
	}{{1, 1}, {2, 1}, {1, 2}}
	if len(res.Order) != len(want) {
		t.Fatalf("order length %d", len(res.Order))
	}
	for i, w := range want {
		if res.Order[i].Track != w.track || res.Order[i].Sequence != w.seq {
			t.Fatalf("order[%d] = %d/%d want %d/%d", i,
				res.Order[i].Track, res.Order[i].Sequence, w.track, w.seq)
		}
	}
	if filepath.IsAbs(res.Order[0].Path) {
		t.Fatalf("path not rebased: %s", res.Order[0].Path)
	}
}

func TestValidateRejectsCorruptHash(t *testing.T) {
	root, m := twoTrackRoot(t)
	m.Frames[0].SHA256 = strings.Repeat("0", 64)
	_, _, err := Validate(m, LoadOptions{RootDir: root, VerifyContent: true})
	f, ok := model.AsFault(err)
	if !ok || f.Code != model.CodeHashMismatch {
		t.Fatalf("err = %v", err)
	}
	if f.Track != m.Frames[0].Track || f.Frame != m.Frames[0].Sequence {
		t.Fatalf("fault location = %d/%d", f.Track, f.Frame)
	}
}

func TestValidateRejectsDuplicates(t *testing.T) {
	root, m := twoTrackRoot(t)
	m.Frames = append(m.Frames, m.Frames[0])
	_, _, err := Validate(m, LoadOptions{RootDir: root})
	f, _ := model.AsFault(err)
	if f == nil || f.Code != model.CodeSeqDuplicate {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateRejectsPathEscapes(t *testing.T) {
	root, m := twoTrackRoot(t)
	for _, escape := range []string{"../outside.bin", "/etc/passwd", "a/../../x.bin"} {
		bad := m
		bad.Frames = append([]model.Frame(nil), m.Frames...)
		bad.Frames[0].Path = escape
		_, _, err := Validate(bad, LoadOptions{RootDir: root})
		f, _ := model.AsFault(err)
		if f == nil || f.Code != model.CodePathEscape {
			t.Errorf("%q: err = %v", escape, err)
		}
	}
}

func TestValidateRejectsSymlinkEscape(t *testing.T) {
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.bin")
	if err := os.WriteFile(secret, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, m := twoTrackRoot(t)
	if err := os.Symlink(secret, filepath.Join(root, "link.bin")); err != nil {
		t.Skip("symlinks unavailable")
	}
	m.Frames[0].Path = "link.bin"
	_, _, err := Validate(m, LoadOptions{RootDir: root})
	f, _ := model.AsFault(err)
	if f == nil || f.Code != model.CodePathEscape {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateRejectsGeometryAndTime(t *testing.T) {
	root, m := twoTrackRoot(t)
	bad := m
	bad.Frames = append([]model.Frame(nil), m.Frames...)
	bad.Frames[2].Width = 9000
	_, _, err := Validate(bad, LoadOptions{RootDir: root})
	if f, _ := model.AsFault(err); f == nil || f.Code != model.CodeSizeBounds {
		t.Fatalf("geometry: %v", err)
	}

	bad = m
	bad.Frames = append([]model.Frame(nil), m.Frames...)
	bad.Frames[0].PTS = -5
	_, _, err = Validate(bad, LoadOptions{RootDir: root})
	if f, _ := model.AsFault(err); f == nil || f.Code != model.CodeTimeInvalid {
		t.Fatalf("time: %v", err)
	}

	bad = m
	bad.Frames = append([]model.Frame(nil), m.Frames...)
	bad.Frames[2].Width = 128
	_, _, err = Validate(bad, LoadOptions{RootDir: root})
	if f, _ := model.AsFault(err); f == nil || f.Code != model.CodeSizeBounds {
		t.Fatalf("track geometry mismatch: %v", err)
	}
}

func TestValidateRejectsSizeMismatch(t *testing.T) {
	root, m := twoTrackRoot(t)
	m.Frames[2].Size = 999
	_, _, err := Validate(m, LoadOptions{RootDir: root, VerifyContent: true})
	if f, _ := model.AsFault(err); f == nil || f.Code != model.CodeManifestInvalid {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadJSONRoundTrip(t *testing.T) {
	root, m := twoTrackRoot(t)
	blob, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "manifest.json")
	if err := os.WriteFile(path, blob, 0o644); err != nil {
		t.Fatal(err)
	}
	res, _, err := Load(path, LoadOptions{RootDir: root, VerifyContent: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Order) != 3 {
		t.Fatalf("frames = %d", len(res.Order))
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "manifest.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"bogus":true,"frames":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(path, LoadOptions{RootDir: root}); err == nil {
		t.Fatal("unknown field accepted")
	}
	if _, _, err := Load(filepath.Join(root, "missing.json"), LoadOptions{RootDir: root}); err == nil {
		t.Fatal("missing manifest accepted")
	}
}
