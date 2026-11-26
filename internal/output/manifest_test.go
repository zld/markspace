package output

import (
	"os"
	"path/filepath"
	"testing"
)

func sampleManifest() Manifest {
	return Manifest{
		Version:    ManifestVersion,
		CacheState: "hit",
		Input: InputSummary{
			Path: "ok.webm", SHA256: "abc", SizeBytes: 1234,
			DocType: "webm", TimecodeScale: 1_000_000,
			TrackCount: 2, BlockCount: 4,
		},
		Blocks: []BlockRecord{
			{TrackNo: 1, ElemOffset: 10, DataOffset: 16, DataSize: 4,
				BlockTimeNS: 0, EventTimeNS: 100, EventLine: 1, TimeErrorNS: -100,
				OutputFrameStart: 0, OutputFrameEnd: 48000},
		},
		Tracks: []TrackDelivery{
			{TrackNo: 1, OutputFile: "track-1.m4a", OutputSHA256: "ff",
				OutputBytes: 99, BitrateKbps: 64, SampleCount: 100, Timescale: 48000,
				IntegratedLUFS: -16.1, TruePeakDBTP: -2.0, GatePassed: true},
		},
		Mapped:     "4/4",
		MaxErrorNS: 500,
	}
}

func TestStagingPublishIsAtomicAndDeterministic(t *testing.T) {
	parent := t.TempDir()
	st, err := NewStaging(parent, "trace-out")
	if err != nil {
		t.Fatal(err)
	}
	st.SetManifest(sampleManifest())
	// A media file standing in for the encoded candidate.
	if err := os.WriteFile(filepath.Join(st.Dir(), "track-1.m4a"),
		[]byte("m4a-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	final, err := st.Publish()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(final); err != nil {
		t.Fatalf("published dir missing: %v", err)
	}
	if _, err := os.Stat(st.Dir()); !os.IsNotExist(err) {
		t.Fatal("staging dir survived publish")
	}
	loaded, err := LoadManifest(final)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Mapped != "4/4" || loaded.Input.BlockCount != 4 {
		t.Fatalf("manifest round trip wrong: %+v", loaded)
	}
	// Re-serialization must be byte-identical (no timestamps in schema).
	raw1, _ := os.ReadFile(filepath.Join(final, "manifest.json"))
	loaded2, _ := LoadManifest(final)
	st2, _ := NewStaging(parent, "trace-out2")
	st2.SetManifest(*loaded2)
	_, _ = st2.Publish()
	raw2, _ := os.ReadFile(filepath.Join(parent, "trace-out2", "manifest.json"))
	if string(raw1) != string(raw2) {
		t.Fatal("manifest serialization is not deterministic")
	}
}

func TestAbortLeavesNoArtifact(t *testing.T) {
	parent := t.TempDir()
	st, _ := NewStaging(parent, "trace-out")
	_ = os.WriteFile(filepath.Join(st.Dir(), "partial.m4a"), []byte("x"), 0o644)
	st.Abort()
	if entries, _ := os.ReadDir(parent); len(entries) != 0 {
		t.Fatalf("abort left artifacts: %v", entries)
	}
}

func TestLoadManifestRejectsBadVersion(t *testing.T) {
	parent := t.TempDir()
	st, _ := NewStaging(parent, "out")
	m := sampleManifest()
	m.Version = 99
	st.SetManifest(m)
	dir, _ := st.Publish()
	if _, err := LoadManifest(dir); err == nil {
		t.Fatal("unsupported manifest version must be rejected")
	}
}

func TestFileSHA256Stable(t *testing.T) {
	parent := t.TempDir()
	p := filepath.Join(parent, "f.bin")
	if err := os.WriteFile(p, []byte("deterministic content"), 0o644); err != nil {
		t.Fatal(err)
	}
	h1, n1, err := FileSHA256(p)
	if err != nil {
		t.Fatal(err)
	}
	h2, n2, _ := FileSHA256(p)
	if h1 != h2 || n1 != n2 || len(h1) != 64 {
		t.Fatalf("sha unstable or wrong length: %s %d", h1, n1)
	}
}
