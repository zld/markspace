package compare_test

import (
	"path/filepath"
	"testing"

	"bbdiff/internal/align"
	"bbdiff/internal/cfg"
	"bbdiff/internal/compare"
	"bbdiff/internal/disasm"
	"bbdiff/internal/elfscan"
	"bbdiff/internal/fixture"
	"bbdiff/internal/reloc"
)

type fixturePair struct {
	oldFile, newFile     *elfscan.File
	oldRelocs, newRelocs reloc.Result
	oldSyms, newSyms     *compare.SymbolIndex
	engine               *disasm.Engine
}

func loadPair(t *testing.T) *fixturePair {
	t.Helper()
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.elf")
	newPath := filepath.Join(dir, "new.elf")
	if err := fixture.Write(oldPath, fixture.Old); err != nil {
		t.Fatal(err)
	}
	if err := fixture.Write(newPath, fixture.New); err != nil {
		t.Fatal(err)
	}
	oldFile, err := elfscan.Read(oldPath, elfscan.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	newFile, err := elfscan.Read(newPath, elfscan.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	e, err := disasm.NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return &fixturePair{
		oldFile: oldFile, newFile: newFile,
		oldRelocs: reloc.Normalize(oldFile), newRelocs: reloc.Normalize(newFile),
		oldSyms: compare.NewSymbolIndex(oldFile), newSyms: compare.NewSymbolIndex(newFile),
		engine: e,
	}
}

func canonFunction(t *testing.T, p *fixturePair, fn *align.Function, old bool) []*compare.CanonBlock {
	t.Helper()
	f, r, s := p.newFile, p.newRelocs, p.newSyms
	if old {
		f, r, s = p.oldFile, p.oldRelocs, p.oldSyms
	}
	sec, ok := f.Section(fn.SectionIdx)
	if !ok {
		t.Fatalf("section %d missing", fn.SectionIdx)
	}
	code := sec.Data[fn.Address-sec.Addr : fn.Address-sec.Addr+fn.Size]
	blocks, _, err := cfg.Build(p.engine, fn.Name, fn.Address, code, cfg.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	return compare.CanonicalizeFunction(blocks, r, s)
}

func alignedFunction(t *testing.T, res align.Result, name string) (align.Pair, bool) {
	t.Helper()
	for _, p := range res.Pairs {
		if p.Old.Name == name || p.New.Name == name {
			return p, true
		}
	}
	return align.Pair{}, false
}

func TestHashIsStableAcrossRelocationShift(t *testing.T) {
	p := loadPair(t)
	res := align.Align(p.oldFile, p.newFile)
	pair, ok := alignedFunction(t, res, "init")
	if !ok {
		t.Fatal("init was not aligned")
	}
	oldBlocks := canonFunction(t, p, pair.Old, true)
	newBlocks := canonFunction(t, p, pair.New, false)
	if len(oldBlocks) != len(newBlocks) {
		t.Fatalf("block counts differ: %d vs %d", len(oldBlocks), len(newBlocks))
	}
	// init sits at the same address and calls verify, whose address moved.
	// The call's relocation is normalised, so the hashes must be equal.
	for i := range oldBlocks {
		if oldBlocks[i].Strict != newBlocks[i].Strict {
			t.Fatalf("block %d hash changed although only a relocated address moved: %s vs %s\n%s",
				i, oldBlocks[i].Strict, newBlocks[i].Strict, textOf(newBlocks[i]))
		}
	}
}

func TestImmediateChangeIsDetected(t *testing.T) {
	p := loadPair(t)
	res := align.Align(p.oldFile, p.newFile)
	pair, ok := alignedFunction(t, res, "verify")
	if !ok {
		t.Fatal("verify was not aligned")
	}
	results := compare.MatchFunction("verify", "verify", canonFunction(t, p, pair.Old, true), canonFunction(t, p, pair.New, false))
	var changed, equal int
	for _, r := range results {
		switch r.Kind {
		case compare.KindChanged, compare.KindUncertain:
			changed++
			if r.ChangedInsns == 0 {
				t.Errorf("changed block at %#x reports 0 changed instructions", r.Block.Address)
			}
		case compare.KindEqual:
			equal++
		}
	}
	if changed != 2 {
		t.Fatalf("changed blocks = %d, want 2 (mov eax immediate and cmp edx immediate)", changed)
	}
	if equal != 5 {
		t.Fatalf("equal blocks = %d, want 5", equal)
	}
}

func TestAddressShiftAloneYieldsNoChanges(t *testing.T) {
	p := loadPair(t)
	res := align.Align(p.oldFile, p.newFile)
	pair, ok := alignedFunction(t, res, "report_error")
	if !ok {
		t.Fatal("report_error was not aligned")
	}
	oldBlocks := canonFunction(t, p, pair.Old, true)
	newBlocks := canonFunction(t, p, pair.New, false)
	results := compare.MatchFunction("report_error", "report_error", oldBlocks, newBlocks)
	for _, r := range results {
		if r.Kind == compare.KindChanged || r.Kind == compare.KindAdded || r.Kind == compare.KindRemoved {
			t.Fatalf("function that only moved reported a %s block at %#x", r.Kind, r.Block.Address)
		}
	}
	if oldBlocks[0].Strict != newBlocks[0].Strict {
		t.Fatal("hash changed for a function that only moved")
	}
}

func TestAddedAndRemoved(t *testing.T) {
	p := loadPair(t)
	res := align.Align(p.oldFile, p.newFile)
	if len(res.Removed) != 1 || res.Removed[0].Name != "legacy_path" {
		t.Fatalf("removed = %+v", res.Removed)
	}
	if len(res.Added) != 1 || res.Added[0].Name != "audit_gate" {
		t.Fatalf("added = %+v", res.Added)
	}
}

func TestUnresolvedRelocationLowersConfidence(t *testing.T) {
	p := loadPair(t)
	res := align.Align(p.oldFile, p.newFile)
	pair, ok := alignedFunction(t, res, "init")
	if !ok {
		t.Fatal("init not aligned")
	}
	// Break the relocation that points at verify so the raw address remains.
	p.newRelocs = reloc.Result{}
	newBlocks := canonFunction(t, p, pair.New, false)
	for _, b := range newBlocks {
		if b.Unresolved == 0 {
			continue
		}
		// Found the call block; make sure the canonical form records why.
		for _, ins := range b.Insns {
			for _, r := range ins.Unresolved {
				if r.Reason == "" {
					t.Error("unresolved reference has no reason")
				}
			}
		}
	}

	oldBlocks := canonFunction(t, p, pair.Old, true)
	results := compare.MatchFunction("init", "init", oldBlocks, newBlocks)
	for _, r := range results {
		if r.Kind != compare.KindUncertain && r.Kind != compare.KindEqual {
			continue
		}
		if r.Counterpart != nil && (r.Canon.Unresolved > 0 || r.Counterpart.Unresolved > 0) {
			if r.Confidence >= 100 {
				t.Fatalf("unresolved block retained confidence %d", r.Confidence)
			}
		}
	}
}

func TestSymbolIndexResolve(t *testing.T) {
	p := loadPair(t)
	idx := p.newSyms
	name, off, ok := idx.Resolve(0x401031)
	if !ok || name != "audit_gate" && name != "verify" {
		t.Fatalf("Resolve(0x401031) = %q+%d, %v", name, off, ok)
	}
	if off < 0 {
		t.Fatalf("negative offset %d", off)
	}
	if _, _, ok := idx.Resolve(0x10); ok {
		t.Fatal("resolved an address outside every section")
	}
	// Section fallback.
	if name, off, ok := idx.Resolve(0x402000); !ok || name != "msg_ok" {
		t.Fatalf("Resolve(0x402000) = %q+%d, %v; want msg_ok", name, off, ok)
	}
	var nilIdx *compare.SymbolIndex
	if _, _, ok := nilIdx.Resolve(1); ok {
		t.Fatal("nil index resolved an address")
	}
}

func TestMatchFunctionDeterministic(t *testing.T) {
	p := loadPair(t)
	res := align.Align(p.oldFile, p.newFile)
	pair, _ := alignedFunction(t, res, "verify")
	oldBlocks := canonFunction(t, p, pair.Old, true)
	newBlocks := canonFunction(t, p, pair.New, false)
	a := compare.MatchFunction("verify", "verify", oldBlocks, newBlocks)
	b := compare.MatchFunction("verify", "verify", oldBlocks, newBlocks)
	if len(a) != len(b) {
		t.Fatal("match count differs between runs")
	}
	for i := range a {
		if a[i].Kind != b[i].Kind || a[i].Block.Address != b[i].Block.Address {
			t.Fatalf("result %d differs: %v@%#x vs %v@%#x", i, a[i].Kind, a[i].Block.Address, b[i].Kind, b[i].Block.Address)
		}
	}
}

func TestEmptyFunctionMatch(t *testing.T) {
	results := compare.MatchFunction("a", "b", nil, nil)
	if len(results) != 0 {
		t.Fatalf("empty match produced %d results", len(results))
	}
}

func textOf(b *compare.CanonBlock) string {
	out := ""
	for _, ins := range b.Insns {
		out += ins.Text + "\n"
	}
	return out
}
