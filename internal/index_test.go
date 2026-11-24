package medpipe

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestDigestDeterministicAndSensitive(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "capture.rtp")
	raw := bytes.Repeat([]byte{0xDE, 0xAD}, 500)
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	d1, err := HashFile(p)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := HashReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !d1.Equal(d2) || d1.Size != int64(len(raw)) || len(d1.SHA256) != 64 {
		t.Fatalf("digest mismatch: %+v %+v", d1, d2)
	}
	changed := append(append([]byte(nil), raw...), 0x01)
	d3, _ := HashReader(bytes.NewReader(changed))
	if d1.Equal(d3) {
		t.Fatal("one-byte change did not alter digest")
	}
	if (Digest{}).Equal(d1) {
		t.Fatal("empty digest compared equal")
	}
}

func sampleIndex(t *testing.T, track string, digest Digest) TrackIndex {
	t.Helper()
	units := []UnitEvent{
		{Frame: Frame{Codec: CodecH264, Keyframe: true, ByteOffset: 4, RawPayload: []byte{1, 2, 3}},
			Sample: SampleRecord{UnifiedUS: 0, OriginalTS: 0, RepairedTS: 0, Anomaly: AnomalyNone}},
		{Frame: Frame{Codec: CodecH264, Keyframe: false, ByteOffset: 20, RawPayload: []byte{4, 5}},
			Sample: SampleRecord{UnifiedUS: 33333, OriginalTS: 3000, RepairedTS: 3000, Anomaly: AnomalyNone}},
		{Frame: Frame{Codec: CodecH264, Keyframe: true, ByteOffset: 36, RawPayload: []byte{6}},
			Sample: SampleRecord{UnifiedUS: 66667, OriginalTS: 6000, RepairedTS: 6000, Anomaly: AnomalyNone}},
	}
	scan := ScanResult{Units: units, ClockStats: ClockStats{}}
	return BuildIndex(track, CodecH264, KindVideo, 90000, digest, scan)
}

func TestIndexBuildSaveLoad(t *testing.T) {
	dir := t.TempDir()
	digest, _ := HashReader(bytes.NewReader([]byte("input-bytes")))
	idx := sampleIndex(t, "v0", digest)
	if idx.FrameCount != 3 || idx.KeyframeCount != 2 || idx.DurationUS != 66667 {
		t.Fatalf("index build wrong: %+v", idx)
	}
	if err := idx.Save(dir); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadIndex(dir, "v0")
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.IsValidFor("v0", digest) {
		t.Fatal("fresh index must be valid for its digest")
	}
	if loaded.IsValidFor("v0", Digest{SHA256: "deadbeef", Size: 11}) {
		t.Fatal("changed digest must invalidate index")
	}
	if loaded.IsValidFor("other-track", digest) {
		t.Fatal("track mismatch must invalidate index")
	}
	kf, ok := loaded.KeyframeAtOrBefore(1)
	if !ok || kf.Ordinal != 0 {
		t.Fatalf("resume seek should land on ordinal 0, got %+v", kf)
	}
	kf, _ = loaded.KeyframeAtOrBefore(2)
	if kf.Ordinal != 2 {
		t.Fatalf("keyframe at or before 2 should be 2, got %d", kf.Ordinal)
	}
}

func TestIndexReuseAndRebuild(t *testing.T) {
	dir := t.TempDir()
	d1, _ := HashReader(bytes.NewReader([]byte("version-1")))
	d2, _ := HashReader(bytes.NewReader([]byte("version-2-changed")))
	idx := sampleIndex(t, "v", d1)
	if err := idx.Save(dir); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadIndex(dir, "v")
	if err != nil {
		t.Fatal(err)
	}
	// Unchanged input: reusable.
	if !loaded.IsValidFor("v", d1) {
		t.Fatal("unchanged input should reuse index")
	}
	// Changed input: rebuild required.
	if loaded.IsValidFor("v", d2) {
		t.Fatal("changed input must force rebuild")
	}
}

func TestPathTraversalRejected(t *testing.T) {
	dir := t.TempDir()
	for _, bad := range []string{"../escape.json", filepath.Join("..", "..", "x")} {
		if _, perr := ResolveLocal(dir, bad); perr == nil || perr.Code != CodePathDenied {
			t.Fatalf("expected path denial for %q, got %v", bad, perr)
		}
	}
	good, perr := ResolveLocal(dir, filepath.Join("sub", "ok.json"))
	if perr != nil {
		t.Fatal(perr)
	}
	if err := AtomicWriteFile(good, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(good); err != nil {
		t.Fatalf("atomic write missing: %v", err)
	}
}

func TestAtomicWriteLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "manifest.json")
	if err := AtomicWriteFile(target, []byte("stable"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.HasPrefix(e.Name(), ".medpipe-tmp-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}
