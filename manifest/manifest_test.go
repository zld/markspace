package manifest

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/clock"
	"mediad/pipeline"
)

func sample() *Manifest {
	m := New()
	m.Inputs = []InputRecord{{Name: "sample.ts", SizeBytes: 1880, ContentHash: "in-hash"}}
	m.Slices = []SliceRecord{{
		TrackID: "pid-0101", StartTicks: 0, EndTicks: clock.FromMs(5),
		StartMs: 0, EndMs: 5, Bytes: 500,
		TargetBitrateBPS: 800000, ActualBitrateBPS: 800000,
	}}
	m.Fingerprints = []FingerprintRecord{{TrackID: "pid-0101", WindowIndex: 0, ContentHash: "fp-1", Reused: false}}
	m.Silences = []SilenceRecord{{TrackID: "pid-0101", StartMs: 10, EndMs: 20}}
	m.Errors = []clock.RangeError{{Kind: clock.KindContinuity, Start: 90, End: 180, Msg: "jump"}}
	m.Checkpoints = []pipeline.Checkpoint{{Key: "k1", OutputHash: "out-1", NodeVersion: pipeline.CodeVersion}}
	m.Stats = Stats{BitrateTargetBPS: 800000, BitrateActualBPS: 800000, QualityErrors: 1, DuplicateCount: 0, CheckpointReused: 0}
	return m
}

func TestManifestRoundTripContainer(t *testing.T) {
	m := sample()
	got, err := RoundTrip(m)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != m.SchemaVersion {
		t.Fatalf("schema = %q, want %q", got.SchemaVersion, m.SchemaVersion)
	}
	if len(got.Slices) != 1 || got.Slices[0].TrackID != "pid-0101" {
		t.Fatalf("slices lost in round trip: %+v", got.Slices)
	}
	if len(got.Fingerprints) != 1 || got.Fingerprints[0].ContentHash != "fp-1" {
		t.Fatalf("fingerprints lost: %+v", got.Fingerprints)
	}
	if len(got.Errors) != 1 || got.Errors[0].Kind != clock.KindContinuity {
		t.Fatalf("errors lost: %+v", got.Errors)
	}
	if got.Checkpoints[0].Key != "k1" {
		t.Fatal("checkpoint provenance lost")
	}
	if got.Stats.BitrateActualBPS != 800000 {
		t.Fatal("stats lost in round trip")
	}
}

func TestManifestContainerHasFtypAndMdmd(t *testing.T) {
	var buf bytes.Buffer
	if err := sample().Write(newBufferSeeker(&buf)); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	// First box must be ftyp; the brand bytes follow immediately.
	if string(b[4:8]) != "ftyp" || string(b[8:12]) != Brand {
		t.Fatalf("container must open with ftyp/%s, got % x", Brand, b[:16])
	}
	if string(b[len(b)-len("mdmd")-4:len(b)-4]) != "mdmd" {
		// mdmd box header sits before its JSON payload.
	}
	found := bytes.Contains(b, []byte("mdmd"))
	if !found {
		t.Fatal("container must contain an mdmd box")
	}
}

func TestManifestDocumentHashDeterministic(t *testing.T) {
	h1, err := sample().DocumentHash()
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := sample().DocumentHash()
	if h1 != h2 || len(h1) != 64 {
		t.Fatalf("identical manifests must hash identically: %q vs %q", h1, h2)
	}
	changed := sample()
	changed.Stats.QualityErrors = 99
	h3, _ := changed.DocumentHash()
	if h3 == h1 {
		t.Fatal("changed content must change the document hash")
	}
}

func TestManifestWriteToFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.mp4")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := sample().Write(f); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	rf, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()
	got, err := Read(rf)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Silences) != 1 || got.Silences[0].EndMs != 20 {
		t.Fatalf("file-backed round trip failed: %+v", got.Silences)
	}
}

func TestReadRejectsContainerWithoutBoxes(t *testing.T) {
	// A bare ftyp box (8-byte header + 8-byte body) with no mdmd must be
	// rejected rather than parsed as an empty manifest.
	ftyp := append([]byte{
		0, 0, 0, 16, 'f', 't', 'y', 'p',
	}, ftypPayload()...)
	if _, err := Read(bytes.NewReader(ftyp)); err == nil {
		t.Fatal("container missing mdmd must fail to parse")
	}
	if _, err := Read(bytes.NewReader([]byte{0, 1, 2, 3, 4})); err == nil {
		t.Fatal("garbage bytes must fail to parse")
	}
}
