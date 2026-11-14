package elfscan_test

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bbdiff/internal/elfscan"
	"bbdiff/internal/fixture"
)

// writeTemp writes data to a temp file and returns its path.
func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustBuild(t *testing.T, v fixture.Variant) []byte {
	t.Helper()
	img, err := fixture.Build(v)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestReadRejectsNonELF(t *testing.T) {
	path := writeTemp(t, "not.elf", []byte("this is not an ELF file"))
	_, err := elfscan.Read(path, elfscan.DefaultOptions())
	if err == nil {
		t.Fatal("Read accepted a non-ELF file")
	}
	var readErr *elfscan.ReadError
	if !errors.As(err, &readErr) {
		t.Fatalf("error is not a *ReadError: %T", err)
	}
}

func TestReadRejectsWrongMachine(t *testing.T) {
	img := mustBuild(t, fixture.Old)
	binary.LittleEndian.PutUint16(img[18:20], uint16(elf.EM_ARM))
	path := writeTemp(t, "arm.elf", img)
	_, err := elfscan.Read(path, elfscan.DefaultOptions())
	if err == nil {
		t.Fatal("Read accepted a non-x86-64 image")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("machine")) {
		t.Fatalf("error does not mention machine: %v", err)
	}
}

func TestReadRejectsBigEndian(t *testing.T) {
	img := mustBuild(t, fixture.Old)
	img[5] = byte(elf.ELFDATA2MSB)
	path := writeTemp(t, "be.elf", img)
	_, err := elfscan.Read(path, elfscan.DefaultOptions())
	if err == nil {
		t.Fatal("Read accepted a big-endian image")
	}
}

func TestReadRejectsELF32(t *testing.T) {
	img := mustBuild(t, fixture.Old)
	img[4] = byte(elf.ELFCLASS32)
	path := writeTemp(t, "elf32.elf", img)
	_, err := elfscan.Read(path, elfscan.DefaultOptions())
	if err == nil {
		t.Fatal("Read accepted an ELF32 image")
	}
}

func TestReadRejectsTruncatedFile(t *testing.T) {
	img := mustBuild(t, fixture.Old)
	// Cut in the middle of .text, so a section extends past EOF.
	path := writeTemp(t, "trunc.elf", img[:len(img)/3])
	_, err := elfscan.Read(path, elfscan.DefaultOptions())
	if err == nil {
		t.Fatal("Read accepted a truncated image")
	}
	var readErr *elfscan.ReadError
	if !errors.As(err, &readErr) {
		t.Fatalf("error is not a *ReadError: %T", err)
	}
}

func TestReadRejectsEmptyFile(t *testing.T) {
	path := writeTemp(t, "empty.elf", nil)
	_, err := elfscan.Read(path, elfscan.DefaultOptions())
	if err == nil {
		t.Fatal("Read accepted an empty file")
	}
}

func TestReadRejectsMissingFile(t *testing.T) {
	_, err := elfscan.Read(filepath.Join(t.TempDir(), "nope.elf"), elfscan.DefaultOptions())
	if err == nil {
		t.Fatal("Read accepted a missing file")
	}
}

func TestReadRejectsDirectory(t *testing.T) {
	_, err := elfscan.Read(t.TempDir(), elfscan.DefaultOptions())
	if err == nil {
		t.Fatal("Read accepted a directory")
	}
}

func TestReadEnforcesFileSizeLimit(t *testing.T) {
	img := mustBuild(t, fixture.Old)
	path := writeTemp(t, "big.elf", img)
	opt := elfscan.DefaultOptions()
	opt.MaxFileSize = int64(len(img) - 1)
	_, err := elfscan.Read(path, opt)
	if err == nil {
		t.Fatal("Read exceeded the configured file size limit")
	}
}

func TestReadZeroLengthSection(t *testing.T) {
	img := mustBuild(t, fixture.Old)
	// Zero out the .text section size (section header 1, size at +32).
	shoff := binary.LittleEndian.Uint64(img[40:48])
	binary.LittleEndian.PutUint64(img[shoff+64+32:shoff+64+40], 0)
	path := writeTemp(t, "zero.elf", img)
	f, err := elfscan.Read(path, elfscan.DefaultOptions())
	if err != nil {
		t.Fatalf("Read rejected a zero-length section: %v", err)
	}
	if f.Sections[1].Size != 0 {
		t.Fatalf(".text size = %d, want 0", f.Sections[1].Size)
	}
	// Every remaining symbol that lived in .text is now out of bounds.
	for _, d := range f.Diagnostics {
		if d.Code == "symbol-in-empty-section" || d.Code == "symbol-out-of-bounds" {
			return
		}
	}
	t.Fatalf("expected an out-of-bounds diagnostic, got %v", f.Diagnostics)
}

func TestReadSymbolOutOfBounds(t *testing.T) {
	img := mustBuild(t, fixture.Old)
	// Move verify's symbol value far outside .text.  Symbol table is section
	// header 4; verify is the 4th entry (index 3) in the fixed order.
	shoff := binary.LittleEndian.Uint64(img[40:48])
	symoff := binary.LittleEndian.Uint64(img[shoff+4*64+24 : shoff+4*64+32])
	binary.LittleEndian.PutUint64(img[symoff+4*24+8:symoff+4*24+16], 0x7fffffff)
	path := writeTemp(t, "oob.elf", img)
	f, err := elfscan.Read(path, elfscan.DefaultOptions())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	found := false
	for _, s := range f.Symbols {
		if s.Name == "verify" && s.OutOfBounds {
			found = true
		}
	}
	if !found {
		t.Fatalf("verify was not flagged out-of-bounds; symbols: %+v", f.Symbols)
	}
}

func TestReadRelocationCyclesDoNotHang(t *testing.T) {
	// Two sections that each claim the other as their relocation target
	// (info/link cycle) must be rejected or ignored deterministically.
	img := mustBuild(t, fixture.Old)
	shoff := binary.LittleEndian.Uint64(img[40:48])
	// .rela.text is section header 3 (info at +44); point it at itself.
	binary.LittleEndian.PutUint32(img[shoff+3*64+44:shoff+3*64+48], 3)
	path := writeTemp(t, "cycle.elf", img)
	f, err := elfscan.Read(path, elfscan.DefaultOptions())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	// Records that now point into the relocation section itself must be
	// rejected by the bounds check, and the reader must terminate.
	reported := false
	for _, d := range f.Diagnostics {
		if d.Code == "reloc-out-of-bounds" || d.Code == "bad-reloc-target-section" {
			reported = true
		}
	}
	if !reported {
		t.Fatalf("cyclic relocation target not reported: %v", f.Diagnostics)
	}
}

func TestReadRelocationBadSymbolIndex(t *testing.T) {
	img := mustBuild(t, fixture.Old)
	shoff := binary.LittleEndian.Uint64(img[40:48])
	relaOff := binary.LittleEndian.Uint64(img[shoff+3*64+24 : shoff+3*64+32])
	// First relocation entry r_info symbol index to 0xffffffff.
	binary.LittleEndian.PutUint64(img[relaOff+8:relaOff+16], 0xffffffff<<32|4)
	path := writeTemp(t, "badsym.elf", img)
	f, err := elfscan.Read(path, elfscan.DefaultOptions())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	reported := false
	for _, d := range f.Diagnostics {
		if d.Code == "reloc-bad-symbol" {
			reported = true
		}
	}
	if !reported {
		t.Fatalf("bad relocation symbol index not reported: %v", f.Diagnostics)
	}
	for _, r := range f.Relocs {
		if r.Unresolved != "" && r.SymbolIndex == 0xffffffff {
			return
		}
	}
	t.Fatal("unresolved relocation was not retained")
}

func TestReadDeterministic(t *testing.T) {
	img := mustBuild(t, fixture.New)
	path := writeTemp(t, "det.elf", img)
	a, err := elfscan.Read(path, elfscan.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	b, err := elfscan.Read(path, elfscan.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Symbols) != len(b.Symbols) || len(a.Relocs) != len(b.Relocs) {
		t.Fatal("two reads disagree on table sizes")
	}
	for i := range a.Symbols {
		if a.Symbols[i] != b.Symbols[i] {
			t.Fatalf("symbol %d differs between reads", i)
		}
	}
	for i := range a.Relocs {
		if a.Relocs[i] != b.Relocs[i] {
			t.Fatalf("relocation %d differs between reads", i)
		}
	}
}

func TestReadRepeatedInput(t *testing.T) {
	// Reading the same path twice in a row must not corrupt state.
	dir := t.TempDir()
	path := filepath.Join(dir, "same.elf")
	if err := fixture.Write(path, fixture.Old); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		f, err := elfscan.Read(path, elfscan.DefaultOptions())
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if len(f.Sections) != 7 {
			t.Fatalf("read %d: %d sections", i, len(f.Sections))
		}
	}
}

func TestSectionBoundsCheck(t *testing.T) {
	img := mustBuild(t, fixture.Old)
	shoff := binary.LittleEndian.Uint64(img[40:48])
	// Give .text an absurd size so it extends past EOF.
	binary.LittleEndian.PutUint64(img[shoff+64+32:shoff+64+40], uint64(len(img))*4)
	path := writeTemp(t, "bounds.elf", img)
	_, err := elfscan.Read(path, elfscan.DefaultOptions())
	if err == nil {
		t.Fatal("Read accepted a section extending past EOF")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("extends past end of file")) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestMutationRobustness feeds deterministically mutated images to the reader
// and the rest of the pipeline must never panic or hang.  Structural failures
// are expected and reported as errors; the only unacceptable outcome is a
// crash or an unbounded wait.
func TestMutationRobustness(t *testing.T) {
	base := mustBuild(t, fixture.Old)
	dir := t.TempDir()
	// A fixed, reproducible pseudo-random mutation set (no global RNG state).
	seed := uint64(0x9e3779b97f4a7c15)
	next := func() uint64 {
		seed ^= seed << 13
		seed ^= seed >> 7
		seed ^= seed << 17
		return seed
	}
	for i := 0; i < 40; i++ {
		img := append([]byte(nil), base...)
		for j := 0; j < 1+int(next()%8); j++ {
			off := int(next() % uint64(len(img)))
			img[off] ^= byte(1 + next()%255)
		}
		path := filepath.Join(dir, "mut.elf")
		if err := os.WriteFile(path, img, 0o644); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := elfscan.Read(path, elfscan.DefaultOptions())
			done <- err
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("mutation %d caused the reader to hang", i)
		}
	}
}
