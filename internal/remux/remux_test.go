package remux

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"mediapack/internal/maptrack"
	"mediapack/internal/model"
)

func testMapping() *maptrack.Mapping {
	mk := func(target uint32, src string, payloads ...[]byte) *maptrack.Binding {
		b := &maptrack.Binding{
			TargetTrack: target, SourcePath: src, SourceTrack: target,
			Format: "mxf", Codec: "vraw",
			Meta: model.TrackMeta{TrackID: target},
		}
		for i, p := range payloads {
			b.Samples = append(b.Samples, model.Sample{
				Source: src, SourceTrack: target, TargetTrack: target,
				Index: uint32(i), DTS: int64(i) * 10, Duration: 10,
				SrcOffset: int64(100 + i*50), Size: uint32(len(p)), Payload: p,
			})
		}
		return b
	}
	return &maptrack.Mapping{Bindings: []*maptrack.Binding{
		mk(1, "a.mxf", []byte("alpha-payload-0001"), []byte("alpha-payload-0002")),
		mk(2, "b.mxf", []byte("beta-payload-00001")),
	}}
}

func sha256File(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return sum[:]
}

func TestWriteLosslessAndProvenance(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.mpkm")
	mf, err := Write(testMapping(), out)
	if err != nil {
		t.Fatal(err)
	}
	if mf.ByteDiff != 0 || mf.SampleCount != 3 || mf.TrackCount != 2 {
		t.Fatalf("manifest wrong: %+v", mf)
	}
	if _, err := os.Stat(out + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("staged tmp file must be removed after commit")
	}
	// Every record carries full source and target offset provenance.
	for _, r := range mf.Records {
		if r.SrcOffset <= 0 || r.TargetOffset <= 0 || r.Size == 0 ||
			r.Source == "" || r.SourceTrack == 0 || r.TargetTrack == 0 {
			t.Fatalf("incomplete provenance: %+v", r)
		}
	}
}

func TestWriteDeterministicBytes(t *testing.T) {
	dir := t.TempDir()
	out1 := filepath.Join(dir, "one.mpkm")
	out2 := filepath.Join(dir, "two.mpkm")
	m1, err := Write(testMapping(), out1)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := Write(testMapping(), out2)
	if err != nil {
		t.Fatal(err)
	}
	h1, h2 := sha256File(t, out1), sha256File(t, out2)
	if string(h1) != string(h2) {
		t.Fatal("identical inputs produced different output bytes")
	}
	if len(m1.Records) != len(m2.Records) {
		t.Fatal("manifest record count diverged")
	}
	for i := range m1.Records {
		if m1.Records[i] != m2.Records[i] {
			t.Fatalf("record %d diverged: %+v vs %+v", i, m1.Records[i], m2.Records[i])
		}
	}
}

func TestWriteRejectsEmptyMapping(t *testing.T) {
	dir := t.TempDir()
	_, err := Write(&maptrack.Mapping{}, filepath.Join(dir, "x.mpkm"))
	me, ok := model.AsMediaError(err)
	if !ok || me.Code != model.CodeMalformedContainer {
		t.Fatalf("want malformed-container, got %v", err)
	}
}

func TestVerifyDetectsCorruption(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.mpkm")
	mf, err := Write(testMapping(), out)
	if err != nil {
		t.Fatal(err)
	}
	// Flip a payload byte in the committed file and re-run verification.
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	rec := mf.Records[0]
	data[rec.TargetOffset] ^= 0xff
	if err := os.WriteFile(out, data, 0o644); err != nil {
		t.Fatal(err)
	}
	// Re-open via the package verifier directly against the mutated file.
	diff, err := verify(out, testMapping(), mf)
	if err != nil {
		t.Fatalf("unexpected structural error: %v", err)
	}
	if diff != 1 {
		t.Fatalf("expected exactly 1 differing byte, got %d", diff)
	}
}
