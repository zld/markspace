package fixture_test

import (
	"bytes"
	"debug/elf"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bbdiff/internal/elfscan"
	"bbdiff/internal/fixture"
)

func TestBuildIsDeterministic(t *testing.T) {
	for _, v := range []fixture.Variant{fixture.Old, fixture.New} {
		a, err := fixture.Build(v)
		if err != nil {
			t.Fatalf("Build(%v): %v", v, err)
		}
		b, err := fixture.Build(v)
		if err != nil {
			t.Fatalf("Build(%v) again: %v", v, err)
		}
		if !bytes.Equal(a, b) {
			t.Fatalf("%v: two builds differ", v)
		}
		if len(a) == 0 {
			t.Fatalf("%v: empty image", v)
		}
	}
}

func TestBuildVariantsDiffer(t *testing.T) {
	oldImg, err := fixture.Build(fixture.Old)
	if err != nil {
		t.Fatal(err)
	}
	newImg, err := fixture.Build(fixture.New)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(oldImg, newImg) {
		t.Fatal("old and new images are identical")
	}
}

func TestImageParsesAsELF(t *testing.T) {
	for _, v := range []fixture.Variant{fixture.Old, fixture.New} {
		img, err := fixture.Build(v)
		if err != nil {
			t.Fatalf("%v: %v", v, err)
		}
		ef, err := elf.NewFile(bytes.NewReader(img))
		if err != nil {
			t.Fatalf("%v: debug/elf rejected the image: %v", v, err)
		}
		if ef.Type != elf.ET_EXEC {
			t.Errorf("%v: type = %v, want ET_EXEC", v, ef.Type)
		}
		if ef.Machine != elf.EM_X86_64 {
			t.Errorf("%v: machine = %v, want EM_X86_64", v, ef.Machine)
		}
		if len(ef.Sections) != 7 {
			t.Errorf("%v: %d sections, want 7", v, len(ef.Sections))
		}
		if _, err := ef.Symbols(); err != nil {
			t.Errorf("%v: Symbols(): %v", v, err)
		}
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.elf")
	if err := fixture.Write(path, fixture.Old); err != nil {
		t.Fatalf("Write: %v", err)
	}
	f, err := elfscan.Read(path, elfscan.DefaultOptions())
	if err != nil {
		t.Fatalf("elfscan.Read: %v", err)
	}
	if len(f.Symbols) == 0 {
		t.Fatal("no symbols decoded")
	}
	if len(f.Relocs) != 3 {
		t.Fatalf("got %d relocations, want 3", len(f.Relocs))
	}
	if len(f.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %v", f.Diagnostics)
	}
	foundLegacy := false
	for _, s := range f.Symbols {
		if s.Name == "legacy_path" {
			foundLegacy = true
			if s.Type != elf.STT_FUNC {
				t.Errorf("legacy_path type = %v, want STT_FUNC", s.Type)
			}
		}
	}
	if !foundLegacy {
		t.Error("old fixture is missing legacy_path")
	}
}

func TestWriteRejectsTraversal(t *testing.T) {
	cases := []string{
		"../escape.elf",
		"a/../../b.elf",
		"./../x.elf",
		"",
		".",
		"..",
	}
	for _, c := range cases {
		if err := fixture.ValidateOutputPath(c); err == nil {
			t.Errorf("ValidateOutputPath(%q) = nil, want error", c)
		}
	}
	ok := filepath.Join(t.TempDir(), "nested", "ok.elf")
	if err := fixture.ValidateOutputPath(ok); err != nil {
		t.Errorf("ValidateOutputPath(%q) = %v, want nil", ok, err)
	}
}

func TestWriteCleansTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.elf")
	if err := fixture.Write(path, fixture.New); err != nil {
		t.Fatalf("Write: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") || strings.Contains(e.Name(), ".tmp") {
			t.Errorf("leftover temporary file: %s", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Errorf("directory has %d entries, want 1", len(entries))
	}
}

func TestWriteReplacesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.elf")
	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fixture.Write(path, fixture.Old); err != nil {
		t.Fatalf("Write: %v", err)
	}
	img, _ := fixture.Build(fixture.Old)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, img) {
		t.Fatal("Write did not replace the existing file with the built image")
	}
}
