// Package reloc normalises PC-relative relocations into symbolic references.
//
// The goal is to make an instruction encoding comparable across two builds
// even when the linker placed a referenced symbol at a different address.
// Each relocation is turned into a Reference: a symbolic name, a kind, and the
// field in the instruction it rewrites.  Anything unusual (unknown relocation
// type, missing symbol, out-of-range field) is preserved verbatim, tagged as
// unresolved, and lowers the confidence reported by the matcher.
package reloc

import (
	"fmt"
	"sort"

	"bbdiff/internal/elfscan"
)

// RefKind describes the normalised form of a relocated field.
type RefKind string

const (
	// KindSymbol means the field was rewritten as "symbol" (plus an offset
	// where the relocation addend is non-zero).
	KindSymbol RefKind = "symbol"
	// KindPLT means the field points at a PLT entry for a named dynamic
	// symbol; PLT entries move between builds, so only the name is kept.
	KindPLT RefKind = "plt"
	// KindRelative means the field is a self-relative adjustment with no
	// meaningful symbol; the field is blanked.
	KindRelative RefKind = "relative"
	// KindAbsolute means the field holds the symbol's address directly and
	// does not depend on the field's own location; it stays comparable as
	// long as the symbol keeps its meaning.
	KindAbsolute RefKind = "absolute"
	// KindUnknown marks a relocation that could not be normalised.  The
	// original bytes are kept and the reference is reported.
	KindUnknown RefKind = "unknown"
)

// Reference is one normalised relocation site.
type Reference struct {
	// FieldAddr is the virtual address of the relocated field (the bytes
	// that contain the displacement or immediate).
	FieldAddr uint64
	// FieldSize is the number of bytes the relocation rewrote.
	FieldSize uint8
	Kind      RefKind
	// Symbol is the symbolic target; empty for KindRelative / KindUnknown.
	Symbol string
	// Addend is the relocation addend after ELF semantics.
	Addend int64
	// PCRelative reports whether the relocation value depends on the field's
	// own address (as PC-relative forms do).
	PCRelative bool
	// RelocType is the raw ELF relocation type.
	RelocType uint32
	// Section is the source section index of the relocation record.
	Section int
	// Reason is set when the reference is not fully resolved.
	Reason string
}

// Unresolved reports whether the reference retains raw, address-dependent
// bytes.
func (r Reference) Unresolved() bool { return r.Kind == KindUnknown || r.Reason != "" }

// String renders the reference for diagnostics and the canonical stream.
func (r Reference) String() string {
	switch r.Kind {
	case KindSymbol:
		if r.Addend != 0 {
			return fmt.Sprintf("[sym %s %+d]", r.Symbol, r.Addend)
		}
		return "[sym " + r.Symbol + "]"
	case KindPLT:
		return "[plt " + r.Symbol + "]"
	case KindAbsolute:
		if r.Addend != 0 {
			return fmt.Sprintf("[abs %s %+d]", r.Symbol, r.Addend)
		}
		return "[abs " + r.Symbol + "]"
	case KindRelative:
		return "[rel]"
	default:
		return fmt.Sprintf("[unknown reloc %d]", r.RelocType)
	}
}

// Result is the outcome of normalising one section's relocations.
type Result struct {
	// References by field address, sorted ascending.
	References []Reference
	byAddr     map[uint64]Reference
	// Diagnostics are deterministic reports about unresolved references.
	Diagnostics []elfscan.Diagnostic
	// Unresolved counts references that could not be normalised.
	Unresolved int
}

// Lookup returns the reference for a relocated field address.
func (r Result) Lookup(addr uint64) (Reference, bool) {
	ref, ok := r.byAddr[addr]
	return ref, ok
}

// FieldWidth maps a known relocation type to the width of the field it
// rewrites.  AMD64 relocation numbers are taken from the System V ABI.
func FieldWidth(typ uint32) (uint8, bool) {
	switch typ {
	case 1: // R_X86_64_64
		return 8, true
	case 2: // R_X86_64_PC32
		return 4, true
	case 4: // R_X86_64_PLT32
		return 4, true
	case 9: // R_X86_64_GOTPCREL
		return 4, true
	case 10: // R_X86_64_32
		return 4, true
	case 11: // R_X86_64_32S
		return 4, true
	case 24: // R_X86_64_PC64
		return 8, true
	case 41: // R_X86_64_GOTPCRELX
		return 4, true
	case 42: // R_X86_64_REX_GOTPCRELX
		return 4, true
	default:
		return 0, false
	}
}

// Normalize builds a relocation index for the whole file.
func Normalize(f *elfscan.File) Result {
	res := Result{byAddr: make(map[uint64]Reference)}

	// Group by section to keep the diagnostics ordered by section index.
	bySection := make(map[int][]elfscan.Reloc)
	for _, r := range f.Relocs {
		bySection[r.Section] = append(bySection[r.Section], r)
	}
	sectionIdx := make([]int, 0, len(bySection))
	for idx := range bySection {
		sectionIdx = append(sectionIdx, idx)
	}
	sort.Ints(sectionIdx)

	for _, idx := range sectionIdx {
		rels := bySection[idx]
		sort.SliceStable(rels, func(i, j int) bool {
			if rels[i].Address != rels[j].Address {
				return rels[i].Address < rels[j].Address
			}
			return rels[i].SourceOffset < rels[j].SourceOffset
		})
		for _, er := range rels {
			ref := normalizeOne(f, er)
			if ref.Unresolved() {
				res.Unresolved++
				res.Diagnostics = append(res.Diagnostics, elfscan.Diagnostic{
					Severity: elfscan.SeverityWarning,
					Code:     "reloc-unresolved",
					Message:  fmt.Sprintf("relocation at 0x%x (type %d): %s", er.Address, er.Type, ref.Reason),
				})
			}
			// Repeated relocations at one address are malformed; the last
			// record wins deterministically after sorting, and we say so.
			if prev, exists := res.byAddr[er.Address]; exists {
				res.Diagnostics = append(res.Diagnostics, elfscan.Diagnostic{
					Severity: elfscan.SeverityWarning,
					Code:     "reloc-duplicate",
					Message:  fmt.Sprintf("duplicate relocation at 0x%x; keeping %s, dropping %s", er.Address, ref.String(), prev.String()),
				})
			}
			res.byAddr[er.Address] = ref
		}
	}

	res.References = make([]Reference, 0, len(res.byAddr))
	for _, ref := range res.byAddr {
		res.References = append(res.References, ref)
	}
	sort.Slice(res.References, func(i, j int) bool {
		return res.References[i].FieldAddr < res.References[j].FieldAddr
	})
	return res
}

func normalizeOne(f *elfscan.File, er elfscan.Reloc) Reference {
	ref := Reference{
		FieldAddr: er.Address,
		RelocType: er.Type,
		Addend:    er.Addend,
		Section:   er.Section,
	}

	if er.Unresolved != "" {
		ref.Kind = KindUnknown
		ref.Reason = er.Unresolved
		return ref
	}

	width, known := FieldWidth(er.Type)
	if !known {
		ref.Kind = KindUnknown
		ref.Reason = fmt.Sprintf("unsupported relocation type %d", er.Type)
		return ref
	}
	ref.FieldSize = width

	sec, ok := f.Section(er.Section)
	if !ok || len(sec.Data) == 0 {
		ref.Kind = KindUnknown
		ref.Reason = "relocation target section is not readable"
		return ref
	}
	if er.Address < sec.Addr {
		ref.Kind = KindUnknown
		ref.Reason = "relocation address precedes its section"
		return ref
	}
	if off := er.Address - sec.Addr; off+uint64(width) > uint64(len(sec.Data)) {
		ref.Kind = KindUnknown
		ref.Reason = fmt.Sprintf("relocated field [0x%x,+%d) exceeds section %s", er.Address, width, sec.Name)
		return ref
	}

	switch er.Type {
	case 2, 4, 9, 24, 41, 42: // PC-relative forms
		if er.SymbolName == "" {
			// Symbol index 0 or an unnamed symbol: rely on the raw bytes.
			ref.Kind = KindUnknown
			ref.Reason = "PC-relative relocation has no symbol name"
			return ref
		}
		if er.Type == 4 || er.Type == 9 || er.Type == 41 || er.Type == 42 {
			ref.Kind = KindPLT
		} else {
			ref.Kind = KindSymbol
		}
		ref.Symbol = er.SymbolName
		ref.PCRelative = true
		return ref
	case 1, 10, 11: // absolute forms: comparable only if the symbol is stable
		if er.SymbolName == "" {
			ref.Kind = KindUnknown
			ref.Reason = "absolute relocation has no symbol name"
			return ref
		}
		ref.Kind = KindAbsolute
		ref.Symbol = er.SymbolName
		return ref
	}

	ref.Kind = KindUnknown
	ref.Reason = fmt.Sprintf("relocation type %d not handled", er.Type)
	return ref
}
