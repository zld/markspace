package align_test

import (
	"path/filepath"
	"testing"

	"bbdiff/internal/align"
	"bbdiff/internal/elfscan"
	"bbdiff/internal/fixture"
)

func readFixture(t *testing.T, v fixture.Variant) *elfscan.File {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, v.String()+".elf")
	if err := fixture.Write(path, v); err != nil {
		t.Fatal(err)
	}
	f, err := elfscan.Read(path, elfscan.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestAlignMatchesFixtures(t *testing.T) {
	oldFile := readFixture(t, fixture.Old)
	newFile := readFixture(t, fixture.New)

	res := align.Align(oldFile, newFile)
	if len(res.Pairs) != 3 {
		t.Fatalf("got %d confident pairs, want 3 (init, verify, report_error): %+v", len(res.Pairs), res.Pairs)
	}
	if len(res.Removed) != 1 || res.Removed[0].Name != "legacy_path" {
		t.Fatalf("removed = %+v, want legacy_path", res.Removed)
	}
	if len(res.Added) != 1 || res.Added[0].Name != "audit_gate" {
		t.Fatalf("added = %+v, want audit_gate", res.Added)
	}
	if len(res.Uncertain) != 0 {
		t.Fatalf("unexpected uncertain pairs: %+v", res.Uncertain)
	}

	// verify and verify_alias share an address, so they collapse into one
	// Function whose alias set contains both names.
	var verify *align.Function
	for _, p := range res.Pairs {
		if p.Old.Name == "verify" {
			verify = p.New
		}
	}
	if verify == nil {
		t.Fatal("verify was not aligned")
	}
	if len(verify.Aliases) != 2 {
		t.Fatalf("verify aliases = %v, want verify and verify_alias", verify.Aliases)
	}
	if verify.Address != 0x401030 {
		t.Errorf("new verify address = %#x, want 0x401030", verify.Address)
	}
}

func TestAlignReportsAmbiguity(t *testing.T) {
	oldFile := readFixture(t, fixture.Old)
	newFile := readFixture(t, fixture.New)

	// Add a second symbol named "verify" at a different address.  Because it
	// shares a name but not address/size evidence, alignment must refuse to
	// guess and report uncertainty instead.
	extra := elfscan.Symbol{
		Table:        4,
		Index:        len(newFile.Symbols),
		Name:         "verify",
		Value:        0x40102a, // inside .text, not at any existing function start
		Size:         4,
		Info:         byte(0x12),
		SectionIndex: 1,
		Type:         2,
		Bind:         1,
		Defined:      true,
		Section:      ".text",
	}
	newFile.Symbols = append(newFile.Symbols, extra)

	res := align.Align(oldFile, newFile)
	if len(res.Uncertain) == 0 {
		t.Fatalf("duplicate name produced no uncertain pairing: pairs=%d", len(res.Pairs))
	}
	var found bool
	for _, d := range res.Diagnostics {
		if d.Code == "ambiguous-symbol" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no ambiguous-symbol diagnostic: %+v", res.Diagnostics)
	}
}

func TestCollectIgnoresInvalidSymbols(t *testing.T) {
	f := readFixture(t, fixture.Old)
	f.Symbols = append(f.Symbols,
		elfscan.Symbol{Name: "bad_undef", Type: 2, Defined: false},
		elfscan.Symbol{Name: "bad_oob", Type: 2, Defined: true, OutOfBounds: true, Size: 8, SectionIndex: 1, Value: 0x401000},
		elfscan.Symbol{Name: "bad_zero", Type: 2, Defined: true, Size: 0, SectionIndex: 1, Value: 0x401000},
		elfscan.Symbol{Name: "bad_data", Type: 1, Defined: true, Size: 8, SectionIndex: 2, Value: 0x402000},
	)
	fns := align.Collect(f)
	for _, fn := range fns {
		for _, alias := range fn.Aliases {
			if len(alias) > 3 && alias[:3] == "bad" {
				t.Errorf("invalid symbol %q was collected as a function", alias)
			}
		}
	}
}

func TestAlignEmptyImages(t *testing.T) {
	// Two images with no function symbols must produce an empty, error-free
	// alignment.
	oldFile := &elfscan.File{Sections: []elfscan.Section{{Index: 0, Name: "", Type: 0}}}
	newFile := &elfscan.File{Sections: []elfscan.Section{{Index: 0, Name: "", Type: 0}}}
	res := align.Align(oldFile, newFile)
	if len(res.Pairs)+len(res.Removed)+len(res.Added)+len(res.Uncertain) != 0 {
		t.Fatalf("expected empty alignment, got %+v", res)
	}
}

func TestAlignDeterministic(t *testing.T) {
	oldFile := readFixture(t, fixture.Old)
	newFile := readFixture(t, fixture.New)
	a := align.Align(oldFile, newFile)
	b := align.Align(oldFile, newFile)
	if len(a.Pairs) != len(b.Pairs) || len(a.Removed) != len(b.Removed) || len(a.Added) != len(b.Added) {
		t.Fatal("two alignment runs disagree")
	}
	for i := range a.Pairs {
		if a.Pairs[i].Old.Name != b.Pairs[i].Old.Name || a.Pairs[i].New.Name != b.Pairs[i].New.Name {
			t.Fatalf("pair %d differs between runs", i)
		}
	}
}
