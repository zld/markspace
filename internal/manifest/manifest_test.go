package manifest_test

import (
	"os"
	"path/filepath"
	"testing"

	"mediapack/internal/fanout"
	"mediapack/internal/manifest"
	"mediapack/internal/model"
)

func sampleManifest() *manifest.Manifest {
	gap := fanout.Gap{SegmentIndex: 1, StartSeq: 4, EndSeq: 5, Count: 2}
	return manifest.Build(17, model.StageExported, "deadbeef", 9000,
		modelAlign(),
		[]*fanout.Representation{
			{
				Layer: fanout.LayerSpec{Name: "low", BitrateKbps: 400, Width: 8, Height: 6,
					SSRC: 1, FramesPerSeg: 2},
				Sent: 6, Received: 4, Duplicates: 0,
				Segments: []fanout.Segment{
					{Index: 0, Layer: "low", TickStart: 0, TickEnd: 100,
						FrameStart: 0, FrameEnd: 1, Complete: true, Seqs: []uint16{0, 1, 2}},
					{Index: 1, Layer: "low", TickStart: 100, TickEnd: 200,
						FrameStart: 2, FrameEnd: 3, Complete: false, Gaps: []fanout.Gap{gap}},
				},
			},
		},
		map[string][]manifest.FrameEntry{
			"low": {
				{Index: 0, Tick: 0, SourceTrack: 0, SourceSample: 0,
					Width: 8, Height: 6, PixelFormat: "RGBA",
					InputDigest: "deadbeef", SHA256: "aaaa"},
				{Index: 1, Tick: 100, SourceTrack: 0, SourceSample: 1,
					Width: 8, Height: 6, PixelFormat: "RGBA",
					InputDigest: "deadbeef", SHA256: "bbbb"},
				{Index: 2, Tick: 200, SourceTrack: 0, SourceSample: 2,
					Width: 8, Height: 6, PixelFormat: "RGBA",
					InputDigest: "deadbeef", SHA256: "cccc"},
				{Index: 3, Tick: 300, SourceTrack: 0, SourceSample: 3,
					Width: 8, Height: 6, PixelFormat: "RGBA",
					InputDigest: "deadbeef", SHA256: "dddd"},
			},
		}, nil)
}

func TestWriteReadVerifyRoundTrip(t *testing.T) {
	root := t.TempDir()
	m := sampleManifest()
	sum, err := manifest.Write(root, m)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.WriteFrameRecords(root, "low", m.Layers[0].Frames); err != nil {
		t.Fatal(err)
	}
	loaded, raw, got, err := manifest.Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != sum || manifest.HashBytes(raw) != sum {
		t.Fatal("checksum sidecar mismatch")
	}
	if loaded.Seed != 17 || loaded.FrameCount != 4 || len(loaded.Layers) != 1 {
		t.Fatalf("loaded manifest wrong: %+v", loaded)
	}
	rep, err := manifest.Verify(root)
	if err != nil || !rep.OK {
		t.Fatalf("verify failed: %+v err=%v", rep, err)
	}
	if rep.FrameChecks != 4 || rep.SegmentChecks != 2 || rep.LayerCount != 1 {
		t.Fatalf("report counters wrong: %+v", rep)
	}
}

func TestDeterministicManifestHash(t *testing.T) {
	hash1 := writeHash(t, t.TempDir())
	hash2 := writeHash(t, t.TempDir())
	if hash1 != hash2 {
		t.Fatal("identical manifests must hash identically across directories")
	}
}

func writeHash(t *testing.T, root string) string {
	t.Helper()
	m := sampleManifest()
	sum, err := manifest.Write(root, m)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

func TestVerifyDetectsTamperedFrame(t *testing.T) {
	root := t.TempDir()
	m := sampleManifest()
	if _, err := manifest.Write(root, m); err != nil {
		t.Fatal(err)
	}
	if err := manifest.WriteFrameRecords(root, "low", m.Layers[0].Frames); err != nil {
		t.Fatal(err)
	}
	// Tamper one frame record without updating the sealed manifest.
	frames := m.Layers[0].Frames
	frames[0].SHA256 = "tampered"
	if err := manifest.WriteFrameRecords(root, "low", frames); err != nil {
		t.Fatal(err)
	}
	rep, err := manifest.Verify(root)
	if err != nil {
		t.Fatalf("verify returns report, got err %v", err)
	}
	if rep.OK || len(rep.Differences) == 0 {
		t.Fatal("tampered frame must produce a verification difference")
	}
}

func TestTamperedManifestRejectedByChecksum(t *testing.T) {
	root := t.TempDir()
	m := sampleManifest()
	if _, err := manifest.Write(root, m); err != nil {
		t.Fatal(err)
	}
	mPath := filepath.Join(root, manifest.FileManifest)
	raw, err := os.ReadFile(mPath)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-2] = '9' // mutate final line content
	if err := os.WriteFile(mPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := manifest.Read(root); !model.IsKind(err, model.KindChecksum) {
		t.Fatalf("expected checksum error, got %v", err)
	}
}

func TestSafePathRejectsEscape(t *testing.T) {
	for _, bad := range []string{"../x", "/etc/passwd", "a/../../b"} {
		if _, err := manifest.SafePath("/tmp/out", bad); err == nil {
			t.Fatalf("unsafe path accepted: %s", bad)
		}
	}
	if _, err := manifest.SafePath("/tmp/out", "layers/low/frames.json"); err != nil {
		t.Fatalf("safe path rejected: %v", err)
	}
}

func TestGuardVerifiedArtifact(t *testing.T) {
	root := t.TempDir()
	// Empty directory: nothing to guard.
	if err := manifest.GuardVerifiedArtifact(root); err != nil {
		t.Fatal(err)
	}
	m := sampleManifest()
	m.Stage = model.StageVerified
	if _, err := manifest.Write(root, m); err != nil {
		t.Fatal(err)
	}
	if err := manifest.GuardVerifiedArtifact(root); err == nil {
		t.Fatal("overwriting a verified artifact must be refused")
	}
}
