package index_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mediacheck/ebml"
	"mediacheck/index"
	"mediacheck/internal/fixture"
	"mediacheck/timeline"
)

func scanRecord(t *testing.T, cfg fixture.Config) (index.Summary, *ebml.ScanResult) {
	t.Helper()
	stream, err := fixture.Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := index.Digest(bytes.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	res, err := ebml.Scan(bytes.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	return sum, res
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "idx")
	store, err := index.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	sum, res := scanRecord(t, fixture.Config{Seed: 11, Frames: 5})
	rec := index.FromScan(sum, res)
	if err := store.Save(sum, rec); err != nil {
		t.Fatal(err)
	}
	got, reason := store.Load(sum)
	if got == nil {
		t.Fatalf("expected cache hit, rebuild reason: %s", reason)
	}
	if len(got.Blocks) != 10 || got.TimeScaleNs != 1_000_000 || got.InputHex != sum.Hex() {
		t.Fatalf("record projection wrong: %+v", got)
	}
	if got.Blocks[0].TimeNs != 0 || got.Blocks[0].TrackNumber != 1 {
		t.Fatalf("first block wrong: %+v", got.Blocks[0])
	}
}

func TestInputChangeInvalidates(t *testing.T) {
	dir := t.TempDir()
	store, _ := index.NewStore(dir)
	sum, res := scanRecord(t, fixture.Config{Seed: 11, Frames: 5})
	if err := store.Save(sum, index.FromScan(sum, res)); err != nil {
		t.Fatal(err)
	}
	other, _ := scanRecord(t, fixture.Config{Seed: 12, Frames: 5})
	// A different input maps to a different index file: plain miss.
	if got, reason := store.Load(other); got != nil || reason != "index missing" {
		t.Fatalf("want missing rebuild, got %v %q", got, reason)
	}
	// Plant A's record under B's file name to exercise the summary check.
	foreign := filepath.Join(dir, other.Hex()+".idx")
	src := filepath.Join(dir, sum.Hex()+".idx")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foreign, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, reason := store.Load(other); got != nil || reason != "input summary changed" {
		t.Fatalf("want summary-changed rebuild, got %v %q", got, reason)
	}
}

func TestVersionMismatchInvalidates(t *testing.T) {
	dir := t.TempDir()
	store, _ := index.NewStore(dir)
	sum, res := scanRecord(t, fixture.Config{Seed: 1, Frames: 2})
	rec := index.FromScan(sum, res)
	if err := store.Save(sum, rec); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, sum.Hex()+".idx")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the version field inside the JSON body (indented output).
	patched := bytes.Replace(raw, []byte(`"version": 1`), []byte(`"version": 99`), 1)
	if bytes.Equal(raw, patched) {
		t.Fatalf("version field not found in index: %s", raw[:min(80, len(raw))])
	}
	if err := os.WriteFile(path, patched, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, reason := store.Load(sum); got != nil || !strings.Contains(reason, "incompatible") {
		t.Fatalf("want version rebuild, got %v %q", got, reason)
	}
}

func TestInterruptedWriteNeverServed(t *testing.T) {
	dir := t.TempDir()
	store, _ := index.NewStore(dir)
	sum, res := scanRecord(t, fixture.Config{Seed: 2, Frames: 2})
	// Simulate a crash before rename: only a trailer-less .tmp exists.
	tmp := filepath.Join(dir, sum.Hex()+".idx.tmp")
	if err := os.WriteFile(tmp, []byte(`{"version":1`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, reason := store.Load(sum); got != nil || reason != "index missing" {
		t.Fatalf("temp file must not be served: %v %q", got, reason)
	}
	// A completed file with stripped trailer is also rejected.
	if err := store.Save(sum, index.FromScan(sum, res)); err != nil {
		t.Fatal(err)
	}
	final := filepath.Join(dir, sum.Hex()+".idx")
	raw, _ := os.ReadFile(final)
	if err := os.WriteFile(final, bytes.TrimSuffix(raw, []byte("\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, reason := store.Load(sum); got != nil || reason != "index write was interrupted" {
		t.Fatalf("want interrupted rebuild, got %v %q", got, reason)
	}
	if err := store.DiscardTemp(sum); err != nil {
		t.Fatal(err)
	}
}

func TestDigestSizeLimit(t *testing.T) {
	big := bytes.NewReader(make([]byte, index.MaxInputSize+1))
	if _, err := index.Digest(big); !timeline.IsCategory(err, timeline.CatOverflow) {
		t.Fatalf("want overflow error, got %v", err)
	}
}

func TestStoreRejectsUnsafePaths(t *testing.T) {
	if _, err := index.NewStore(""); err == nil {
		t.Fatal("empty dir accepted")
	}
	if _, err := index.NewStore("/"); err == nil {
		t.Fatal("filesystem root accepted")
	}
}
