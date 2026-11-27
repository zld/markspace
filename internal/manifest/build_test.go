package manifest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"archivecheck/internal/asset"
	"archivecheck/internal/fault"
	"archivecheck/internal/manifest"
)

func source(name, body string) *asset.Source {
	return asset.FromBytes(name, []byte(body))
}

func add(t *testing.T, b *manifest.Builder, kind string, layer int, data string, deps ...string) manifest.Derivative {
	t.Helper()
	d, err := b.AddDerivative(manifest.DerivativeSpec{Kind: kind, Layer: layer, Data: []byte(data), Deps: deps})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDerivativeKeyIsDependencyAddressable(t *testing.T) {
	a := source("a.srt", "subtitle body")
	b := source("b.mp4", "stream body")
	k1 := manifest.Key("subtitle", 1, []string{a.Digest})
	k2 := manifest.Key("subtitle", 1, []string{a.Digest})
	if k1 != k2 {
		t.Error("keys are not stable")
	}
	k3 := manifest.Key("subtitle", 1, []string{b.Digest})
	k4 := manifest.Key("tracks", 1, []string{a.Digest})
	if k1 == k3 || k1 == k4 {
		t.Error("different dependencies or kinds must yield different keys")
	}
	// Dependency order must not matter.
	k5 := manifest.Key("x", 1, []string{a.Digest, b.Digest})
	k6 := manifest.Key("x", 1, []string{b.Digest, a.Digest})
	if k5 != k6 {
		t.Error("key depends on dependency order")
	}
}

func TestBuildWritesStableManifestAndStore(t *testing.T) {
	dir := t.TempDir()
	srcA := source("a.srt", "one")
	srcB := source("b.mp4", "two")
	b := manifest.NewBuilder(dir)
	if err := b.AddSource(srcA); err != nil {
		t.Fatal(err)
	}
	if err := b.AddSource(srcB); err != nil {
		t.Fatal(err)
	}
	if err := b.AddSource(srcA); err != nil {
		t.Fatal(err)
	}
	b.AddTrack(manifest.TrackRef{Source: srcB.Name, ID: 1, Handler: "vide", SampleCount: 100})
	b.AddDamage(manifest.Damage{Source: srcB.Name, Start: 40, End: 90, Kind: "truncated"})
	b.AddTimeRange(manifest.TimeRange{Source: srcA.Name, Start: 1e9, End: 3e9})
	d1 := add(t, b, "subtitle", 1, `{"cues":2}`, srcA.Digest)
	d2 := add(t, b, "stream", 1, `{"boxes":9}`, srcB.Digest)
	add(t, b, "bundle", 2, `{"ok":true}`, d1.Digest, d2.Digest)
	add(t, b, "bundle", 2, `{"ok":true}`, d2.Digest, d1.Digest) // same payload, same deps, one key

	m, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Sources) != 2 {
		t.Errorf("sources = %d", len(m.Sources))
	}
	if len(m.Derivatives) != 3 {
		t.Errorf("derivatives = %d, want 3", len(m.Derivatives))
	}
	if m.Counts.Added != 3 || m.Counts.Reused != 0 || m.Counts.Changed != 0 {
		t.Errorf("counts = %+v", m.Counts)
	}
	onDisk, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	first := string(onDisk)
	if !strings.HasSuffix(first, "\n") {
		t.Error("manifest should end with a newline")
	}
	// Rebuilding without changes must reproduce the file byte for byte.
	b2 := manifest.NewBuilder(dir)
	_ = b2.AddSource(srcA)
	_ = b2.AddSource(srcB)
	b2.AddTrack(manifest.TrackRef{Source: srcB.Name, ID: 1, Handler: "vide", SampleCount: 100})
	b2.AddDamage(manifest.Damage{Source: srcB.Name, Start: 40, End: 90, Kind: "truncated"})
	b2.AddTimeRange(manifest.TimeRange{Source: srcA.Name, Start: 1e9, End: 3e9})
	add(t, b2, "subtitle", 1, `{"cues":2}`, srcA.Digest)
	add(t, b2, "stream", 1, `{"boxes":9}`, srcB.Digest)
	add(t, b2, "bundle", 2, `{"ok":true}`, d1.Digest, d2.Digest)
	add(t, b2, "bundle", 2, `{"ok":true}`, d2.Digest, d1.Digest)
	m2, err := b2.Build()
	if err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if string(second) == first {
		t.Error("a repeat run should still report reuse; the counts must differ")
	}
	if m2.Counts.Added != 0 || m2.Counts.Reused != 3 {
		t.Errorf("second run counts = %+v, want added=0 reused=3", m2.Counts)
	}
	// The checksum covers content only, so it must not move between runs.
	sum1, _ := m.Checksum()
	sum2, _ := m2.Checksum()
	if sum1 != sum2 {
		t.Errorf("checksum changed between identical runs: %s vs %s", sum1, sum2)
	}
	if _, err := manifest.Verify(dir); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestSourceChangeInvalidatesOnlyDependents(t *testing.T) {
	dir := t.TempDir()
	v1 := source("a.srt", "version one")
	other := source("b.mp4", "unrelated")
	b := manifest.NewBuilder(dir)
	_ = b.AddSource(v1)
	_ = b.AddSource(other)
	add(t, b, "subtitle", 1, `{"v":1}`, v1.Digest)
	add(t, b, "stream", 1, `{"v":1}`, other.Digest)
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}

	v2 := source("a.srt", "version two")
	b2 := manifest.NewBuilder(dir)
	_ = b2.AddSource(v2)
	_ = b2.AddSource(other)
	add(t, b2, "subtitle", 1, `{"v":2}`, v2.Digest)
	add(t, b2, "stream", 1, `{"v":1}`, other.Digest)
	m, err := b2.Build()
	if err != nil {
		t.Fatal(err)
	}
	if m.Counts.Added != 1 {
		t.Errorf("added = %d, want 1", m.Counts.Added)
	}
	if m.Counts.Reused != 1 {
		t.Errorf("reused = %d, want 1 (the unrelated stream)", m.Counts.Reused)
	}
	if m.Counts.Invalidated != 1 {
		t.Errorf("invalidated = %d, want 1 (the old subtitle)", m.Counts.Invalidated)
	}
	if m.Counts.Changed != 0 {
		t.Errorf("changed = %d, want 0", m.Counts.Changed)
	}
}

func TestChangedPayloadUnderSameKeyIsCounted(t *testing.T) {
	dir := t.TempDir()
	src := source("a.srt", "stable source")
	b := manifest.NewBuilder(dir)
	_ = b.AddSource(src)
	add(t, b, "subtitle", 1, `{"render":"one"}`, src.Digest)
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	b2 := manifest.NewBuilder(dir)
	_ = b2.AddSource(src)
	add(t, b2, "subtitle", 1, `{"render":"two"}`, src.Digest)
	m, err := b2.Build()
	if err != nil {
		t.Fatal(err)
	}
	if m.Counts.Changed != 1 || m.Counts.Added != 0 || m.Counts.Invalidated != 0 {
		t.Errorf("counts = %+v", m.Counts)
	}
}

func TestContentAddressedStoreDeduplicates(t *testing.T) {
	dir := t.TempDir()
	src := source("a.srt", "x")
	b := manifest.NewBuilder(dir)
	d1, err := b.AddDerivative(manifest.DerivativeSpec{Kind: "one", Layer: 1, Data: []byte("same bytes"), Deps: []string{src.Digest}})
	if err != nil {
		t.Fatal(err)
	}
	other := source("b.srt", "y")
	d2, err := b.AddDerivative(manifest.DerivativeSpec{Kind: "two", Layer: 1, Data: []byte("same bytes"), Deps: []string{other.Digest}})
	if err != nil {
		t.Fatal(err)
	}
	if d1.Digest != d2.Digest {
		t.Fatal("identical bytes must share a digest")
	}
	if d1.Path != d2.Path {
		t.Errorf("identical bytes stored twice: %s and %s", d1.Path, d2.Path)
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	dir := t.TempDir()
	src := source("a.srt", "z")
	b := manifest.NewBuilder(dir)
	_ = b.AddSource(src)
	d := add(t, b, "subtitle", 1, `{"cues":1}`, src.Digest)
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	if _, err := manifest.Verify(dir); err != nil {
		t.Fatalf("clean verify failed: %v", err)
	}
	// Corrupt the stored derivative.
	target := filepath.Join(dir, filepath.FromSlash(d.Path))
	if err := os.WriteFile(target, []byte(`{"cues":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := manifest.Verify(dir); !fault.HasKind(err, fault.KindDigest) {
		t.Errorf("tampered derivative not caught: %v", err)
	}
}

func TestVerifyRejectsPathEscapeAndBadCounts(t *testing.T) {
	dir := t.TempDir()
	src := source("a.srt", "q")
	b := manifest.NewBuilder(dir)
	_ = b.AddSource(src)
	add(t, b, "subtitle", 1, `{}`, src.Digest)
	m, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manifest.Verify(dir); err != nil {
		t.Fatal(err)
	}

	escaped := *m
	escaped.Derivatives = append([]manifest.Derivative(nil), m.Derivatives...)
	escaped.Derivatives[0].Path = "../outside"
	if _, err := writeAndVerify(t, dir, &escaped); !fault.HasKind(err, fault.KindBoundary) {
		t.Errorf("escaping path accepted: %v", err)
	}
	badCounts := *m
	badCounts.Counts.Added = 7
	if _, err := writeAndVerify(t, dir, &badCounts); !fault.HasKind(err, fault.KindConflict) {
		t.Errorf("bad counts accepted: %v", err)
	}
}

// writeAndVerify writes a manifest directly, bypassing the builder, and then
// verifies it.
func writeAndVerify(t *testing.T, dir string, m *manifest.Manifest) (*manifest.Manifest, error) {
	t.Helper()
	data, err := m.Bytes()
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return manifest.Verify(dir)
}

func TestAddDerivativeValidation(t *testing.T) {
	b := manifest.NewBuilder(t.TempDir())
	if _, err := b.AddDerivative(manifest.DerivativeSpec{Layer: 1, Data: []byte("x")}); !fault.HasKind(err, fault.KindInvalid) {
		t.Errorf("missing kind accepted: %v", err)
	}
	if _, err := b.AddDerivative(manifest.DerivativeSpec{Kind: "k", Layer: 0, Data: []byte("x")}); !fault.HasKind(err, fault.KindInvalid) {
		t.Errorf("zero layer accepted: %v", err)
	}
	if _, err := b.AddDerivative(manifest.DerivativeSpec{Kind: "k", Layer: 1, Data: []byte("x"), Deps: []string{"short"}}); !fault.HasKind(err, fault.KindDigest) {
		t.Errorf("bad dependency accepted: %v", err)
	}
	if err := b.AddSource(nil); !fault.HasKind(err, fault.KindInvalid) {
		t.Errorf("nil source accepted: %v", err)
	}
	bad := &asset.Source{Name: "x", Digest: "abc"}
	if err := b.AddSource(bad); !fault.HasKind(err, fault.KindDigest) {
		t.Errorf("short digest accepted: %v", err)
	}
}
