package media_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/zld/medcheck/pkg/media"
)

func TestImmutableSourceReplacementKeepsOldAsset(t *testing.T) {
	root := t.TempDir()
	reg, err := media.NewRegistry(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(root, "cam-a.h264")
	logical := "cam-a/2026/event.h264"

	v1 := []byte("annex-b-stream-version-1")
	if err := os.WriteFile(src, v1, 0o644); err != nil {
		t.Fatal(err)
	}
	fp1 := media.Fingerprint{StreamSHA256: media.SHA256Bytes(v1).Value}
	a1, err := reg.RegisterFile(logical, src, fp1)
	if err != nil {
		t.Fatal(err)
	}

	v2 := append(v1, []byte("-replaced")...)
	if err := os.WriteFile(src, v2, 0o644); err != nil {
		t.Fatal(err)
	}
	fp2 := media.Fingerprint{StreamSHA256: media.SHA256Bytes(v2).Value}
	a2, err := reg.RegisterFile(logical, src, fp2)
	if err != nil {
		t.Fatal(err)
	}
	if a1.ID == a2.ID {
		t.Fatalf("replacement reused digest %s", a1.ID)
	}

	h, err := reg.History(logical)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.AssetIDs) != 2 || h.Current != a2.ID || h.AssetIDs[0] != a1.ID {
		t.Fatalf("unexpected history: %+v", h)
	}
	old, err := reg.LoadAsset(a1.ID)
	if err != nil {
		t.Fatalf("old asset was not retained: %v", err)
	}
	if old.ID != a1.ID || old.Size != int64(len(v1)) {
		t.Fatalf("old asset mismatch: %+v", old)
	}
	if _, err := os.Stat(filepath.Join(reg.Root(), "objects", a1.ID[:2], a1.ID)); err != nil {
		t.Fatalf("old blob missing: %v", err)
	}
}

func TestFingerprintCacheInvalidationMatrix(t *testing.T) {
	old := media.Fingerprint{StreamSHA256: "s1", ContainerSHA256: "c1", MetadataSHA256: "m1"}

	containerOnly := old
	containerOnly.ContainerSHA256 = "c2"
	st := media.ClassifyFingerprintChange(old, containerOnly)
	if st.StreamParse != "hit" || st.OffsetMapping != "invalid" || st.TimelineIndex != "valid" {
		t.Fatalf("container change matrix wrong: %+v", st)
	}

	metadataOnly := old
	metadataOnly.MetadataSHA256 = "m2"
	st = media.ClassifyFingerprintChange(old, metadataOnly)
	if st.StreamParse != "hit" || st.TimelineIndex != "invalid" {
		t.Fatalf("metadata change matrix wrong: %+v", st)
	}

	streamChange := old
	streamChange.StreamSHA256 = "s2"
	st = media.ClassifyFingerprintChange(old, streamChange)
	if st.StreamParse != "miss" || st.OffsetMapping != "invalid" || st.TimelineIndex != "invalid" {
		t.Fatalf("stream change matrix wrong: %+v", st)
	}
}

func TestNormalizedMetadataRoundTripAndDigest(t *testing.T) {
	m := media.NormalizedMetadata{MovieTimescale: 1000, Tracks: []media.TrackMetadata{
		{TrackID: 2, Codec: "aac", Timescale: 48000},
		{TrackID: 1, Codec: "h264", Timescale: 90000, Width: 1920, Height: 1080},
	}}
	d1, err := m.Digest()
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := media.CanonicalDigest(&m)
	if err != nil {
		t.Fatal(err)
	}
	var back media.NormalizedMetadata
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	d2, err := back.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Fatalf("metadata digest changed after round trip: %s != %s", d1, d2)
	}
	if len(back.Tracks) != 2 || back.Tracks[0].TrackID != 1 {
		t.Fatalf("tracks not normalized/sorted: %+v", back.Tracks)
	}
}

func TestSafeJoinRejectsTraversal(t *testing.T) {
	if _, err := media.SafeJoin(t.TempDir(), "../escape"); err == nil {
		t.Fatal("expected traversal rejection")
	}
	p, err := media.SafeJoin(t.TempDir(), "segments/0.sha256")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(p) {
		t.Fatalf("expected absolute safe path, got %s", p)
	}
}
