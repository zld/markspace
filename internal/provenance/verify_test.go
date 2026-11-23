package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

type fixture struct {
	dir      string
	artifact string
	input    string
	rec      *Record
	exp      Expected
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	artifactBytes := []byte(`{"segments":[{"index":0}]}`)
	inputBytes := bytesForTest(2048)
	artifact := filepath.Join(dir, "manifest.json")
	input := filepath.Join(dir, "input.mkv")
	if err := os.WriteFile(artifact, artifactBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, inputBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	segHash := sha(inputBytes[100:900])
	rec := &Record{
		Version:        1,
		Artifact:       "manifest.json",
		ArtifactSHA256: sha(artifactBytes),
		ManifestSHA256: sha(artifactBytes),
		ConfigDigest:   sha([]byte("config")),
		InputDigest:    sha(inputBytes),
		Inputs:         []InputRef{{Path: "input.mkv", SHA256: segHash, Span: Span{Offset: 100, Length: 800}}},
		Checkpoints:    []CheckpointRef{{ID: "cp-0", Offset: 512, Digest: sha([]byte("cp"))}},
		Segments:       1,
		SegmentHashes:  []string{segHash},
	}
	exp := Expected{
		ArtifactPath:   artifact,
		ArtifactSHA256: sha(artifactBytes),
		ManifestSHA256: sha(artifactBytes),
		ConfigDigest:   sha([]byte("config")),
		SegmentHashes:  []string{segHash},
		InputPaths: map[string]func() (io.ReadCloser, error){
			"input.mkv": func() (io.ReadCloser, error) { return os.Open(input) },
		},
	}
	return &fixture{dir: dir, artifact: artifact, input: input, rec: rec, exp: exp}
}

func bytesForTest(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i%253 + 1)
	}
	return b
}

func TestVerifyClean(t *testing.T) {
	f := newFixture(t)
	if failures := Verify(f.rec, f.exp); len(failures) != 0 {
		t.Fatalf("clean record failed: %v", failures)
	}
}

func TestVerifyDetectsChangedInputSpan(t *testing.T) {
	f := newFixture(t)
	b, _ := os.ReadFile(f.input)
	b[200] ^= 0xFF
	if err := os.WriteFile(f.input, b, 0o644); err != nil {
		t.Fatal(err)
	}
	failures := Verify(f.rec, f.exp)
	if len(failures) != 1 || failures[0].Kind != FailInputSpan {
		t.Fatalf("failures = %v", failures)
	}
}

func TestVerifyDetectsArtifactTamper(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(f.artifact, []byte(`{"segments":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	failures := Verify(f.rec, f.exp)
	found := false
	for _, fl := range failures {
		if fl.Kind == FailArtifactDigest {
			found = true
		}
	}
	if !found {
		t.Fatalf("artifact tamper not detected: %v", failures)
	}
}

func TestVerifyDetectsSegmentHashTamper(t *testing.T) {
	f := newFixture(t)
	f.rec.SegmentHashes = []string{sha([]byte("forged"))}
	failures := Verify(f.rec, f.exp)
	if len(failures) != 1 || failures[0].Kind != FailSegmentHash {
		t.Fatalf("failures = %v", failures)
	}
}

func TestVerifyDetectsConfigMismatch(t *testing.T) {
	f := newFixture(t)
	f.rec.ConfigDigest = sha([]byte("other config"))
	failures := Verify(f.rec, f.exp)
	if len(failures) != 1 || failures[0].Kind != FailConfigDigest {
		t.Fatalf("failures = %v", failures)
	}
}

func TestVerifyRequiresInputs(t *testing.T) {
	f := newFixture(t)
	f.rec.Inputs = nil
	failures := Verify(f.rec, f.exp)
	if len(failures) != 1 || failures[0].Kind != FailMissing {
		t.Fatalf("failures = %v", failures)
	}
}

func TestRecordValidate(t *testing.T) {
	f := newFixture(t)
	if err := f.rec.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	bad := *f.rec
	bad.SegmentHashes = nil
	if err := bad.Validate(); err == nil {
		t.Fatal("expected error for hash count mismatch")
	}
	bad = *f.rec
	bad.ArtifactSHA256 = "nothex"
	if err := bad.Validate(); err == nil {
		t.Fatal("expected error for malformed digest")
	}
}

func TestRecordRoundtripCanonical(t *testing.T) {
	f := newFixture(t)
	// Different input order must canonicalize identically.
	shuffled := *f.rec
	shuffled.Inputs = append([]InputRef(nil), f.rec.Inputs...)
	shuffled.Checkpoints = append([]CheckpointRef(nil), f.rec.Checkpoints...)
	a, err := f.rec.MarshalCanonical()
	if err != nil {
		t.Fatal(err)
	}
	b, err := shuffled.MarshalCanonical()
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("canonical form differs:\n%s\n%s", a, b)
	}
	path := filepath.Join(f.dir, "prov.json")
	if err := f.rec.WriteFile(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ArtifactSHA256 != f.rec.ArtifactSHA256 {
		t.Fatal("roundtrip lost fields")
	}
}

func TestVerifyNilRecord(t *testing.T) {
	failures := Verify(nil, Expected{})
	if len(failures) != 1 || failures[0].Kind != FailMissing {
		t.Fatalf("failures = %v", failures)
	}
}

func TestVerifyRejectsDuplicateCheckpoint(t *testing.T) {
	f := newFixture(t)
	f.rec.Checkpoints = append(f.rec.Checkpoints, f.rec.Checkpoints[0])
	failures := Verify(f.rec, f.exp)
	found := false
	for _, fl := range failures {
		if fl.Kind == FailCheckpoint {
			found = true
		}
	}
	if !found {
		t.Fatalf("duplicate checkpoint not caught: %v", failures)
	}
}

func TestVerifyRejectsMalformedCheckpointDigest(t *testing.T) {
	f := newFixture(t)
	f.rec.Checkpoints[0].Digest = "short"
	failures := Verify(f.rec, f.exp)
	if len(failures) == 0 || failures[0].Kind != FailCheckpoint {
		t.Fatalf("failures = %v", failures)
	}
}
