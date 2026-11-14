package reloc_test

import (
	"path/filepath"
	"testing"

	"bbdiff/internal/elfscan"
	"bbdiff/internal/fixture"
	"bbdiff/internal/reloc"
)

func readFixture(t *testing.T, v fixture.Variant) *elfscan.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), v.String()+".elf")
	if err := fixture.Write(path, v); err != nil {
		t.Fatal(err)
	}
	f, err := elfscan.Read(path, elfscan.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestNormalizeFixture(t *testing.T) {
	f := readFixture(t, fixture.Old)
	res := reloc.Normalize(f)
	if len(res.References) != 3 {
		t.Fatalf("got %d references, want 3", len(res.References))
	}
	if res.Unresolved != 0 {
		t.Fatalf("unresolved = %d, want 0: %v", res.Unresolved, res.Diagnostics)
	}

	byAddr := make(map[uint64]reloc.Reference)
	for _, r := range res.References {
		byAddr[r.FieldAddr] = r
	}
	call, ok := byAddr[0x40100e]
	if !ok {
		t.Fatalf("missing reference for the init->verify call: %v", res.References)
	}
	if call.Kind != reloc.KindPLT || call.Symbol != "verify" {
		t.Errorf("call reference = %+v, want PLT reloc to verify", call)
	}
	if call.FieldSize != 4 {
		t.Errorf("call field size = %d, want 4", call.FieldSize)
	}

	lea, ok := byAddr[0x401032]
	if !ok {
		t.Fatalf("missing reference for the lea msg_err: %v", res.References)
	}
	if lea.Kind != reloc.KindSymbol || lea.Symbol != "msg_err" {
		t.Errorf("lea reference = %+v, want PC32 reloc to msg_err", lea)
	}
}

func TestNormalizeIsStableAcrossBuilds(t *testing.T) {
	oldRes := reloc.Normalize(readFixture(t, fixture.Old))
	newRes := reloc.Normalize(readFixture(t, fixture.New))
	// Both binaries reference verify and msg_err by name.  The field addresses
	// differ because verify shifted, but the symbols must match.
	oldSymbols := symbols(oldRes)
	newSymbols := symbols(newRes)
	if len(oldSymbols) != len(newSymbols) {
		t.Fatalf("reference counts differ: %d vs %d", len(oldSymbols), len(newSymbols))
	}
	for i := range oldSymbols {
		if oldSymbols[i] != newSymbols[i] {
			t.Fatalf("symbol set differs at %d: %q vs %q", i, oldSymbols[i], newSymbols[i])
		}
	}
}

func symbols(res reloc.Result) []string {
	var out []string
	for _, r := range res.References {
		if r.Symbol != "" {
			out = append(out, r.Symbol)
		}
	}
	return out
}

func TestUnknownRelocationType(t *testing.T) {
	f := readFixture(t, fixture.Old)
	f.Relocs[0].Type = 999
	res := reloc.Normalize(f)
	if res.Unresolved != 1 {
		t.Fatalf("unresolved = %d, want 1", res.Unresolved)
	}
	var found bool
	for _, d := range res.Diagnostics {
		if d.Code == "reloc-unresolved" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no reloc-unresolved diagnostic: %v", res.Diagnostics)
	}
	ref, ok := res.Lookup(f.Relocs[0].Address)
	if !ok {
		t.Fatal("unresolved reference was dropped instead of preserved")
	}
	if ref.Kind != reloc.KindUnknown || ref.Reason == "" {
		t.Fatalf("reference = %+v, want KindUnknown with a reason", ref)
	}
}

func TestMissingSymbolName(t *testing.T) {
	f := readFixture(t, fixture.Old)
	f.Relocs[0].SymbolName = ""
	res := reloc.Normalize(f)
	if res.Unresolved != 1 {
		t.Fatalf("unresolved = %d, want 1", res.Unresolved)
	}
	ref, _ := res.Lookup(f.Relocs[0].Address)
	if ref.Kind != reloc.KindUnknown {
		t.Fatalf("reference = %+v, want KindUnknown", ref)
	}
}

func TestBadSymbolIndex(t *testing.T) {
	f := readFixture(t, fixture.Old)
	f.Relocs[0].Unresolved = "symbol index out of range"
	res := reloc.Normalize(f)
	if res.Unresolved != 1 {
		t.Fatalf("unresolved = %d, want 1", res.Unresolved)
	}
	ref, _ := res.Lookup(f.Relocs[0].Address)
	if ref.Reason != "symbol index out of range" {
		t.Fatalf("reason = %q", ref.Reason)
	}
}

func TestFieldOutsideSection(t *testing.T) {
	f := readFixture(t, fixture.Old)
	f.Relocs[0].Address = f.Sections[f.Relocs[0].Section].Addr + f.Sections[f.Relocs[0].Section].Size - 1
	res := reloc.Normalize(f)
	ref, ok := res.Lookup(f.Relocs[0].Address)
	if !ok {
		t.Fatal("out-of-range reference was dropped")
	}
	if ref.Kind != reloc.KindUnknown {
		t.Fatalf("reference = %+v, want KindUnknown", ref)
	}
	if res.Unresolved == 0 {
		t.Fatal("out-of-range field did not lower confidence")
	}
}

func TestDuplicateRelocationIsReported(t *testing.T) {
	f := readFixture(t, fixture.Old)
	dup := f.Relocs[0]
	dup.SourceOffset = 999
	f.Relocs = append(f.Relocs, dup)
	res := reloc.Normalize(f)
	var found bool
	for _, d := range res.Diagnostics {
		if d.Code == "reloc-duplicate" {
			found = true
		}
	}
	if !found {
		t.Fatalf("duplicate relocation not reported: %v", res.Diagnostics)
	}
	// The result must be deterministic: one reference per address.
	if len(res.References) != 3 {
		t.Fatalf("got %d references, want 3 (duplicates collapsed)", len(res.References))
	}
}

func TestAbsoluteRelocationWithSymbol(t *testing.T) {
	f := readFixture(t, fixture.Old)
	addr := f.Relocs[0].Address
	f.Relocs[0].Type = 1 // R_X86_64_64
	res := reloc.Normalize(f)
	ref, ok := res.Lookup(addr)
	if !ok {
		t.Fatal("absolute relocation vanished")
	}
	if ref.Kind != reloc.KindAbsolute || ref.FieldSize != 8 {
		t.Fatalf("reference = %+v, want 8-byte absolute symbol reference", ref)
	}
	if ref.PCRelative {
		t.Fatalf("absolute relocation was marked PC-relative: %+v", ref)
	}
}

func TestEmptyRelocations(t *testing.T) {
	f := readFixture(t, fixture.Old)
	f.Relocs = nil
	res := reloc.Normalize(f)
	if len(res.References) != 0 {
		t.Fatalf("got %d references, want 0", len(res.References))
	}
	if res.Unresolved != 0 {
		t.Fatalf("unresolved = %d, want 0", res.Unresolved)
	}
}
