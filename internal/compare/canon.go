// Package compare canonicalises decoded instructions and matches basic blocks
// between two aligned functions.
//
// Canonicalisation removes address-dependent fields while keeping everything
// that carries meaning: opcode bytes, registers, non-address immediates, and
// control-flow structure.  A relocated field is blanked and replaced by a
// symbolic token (for example "[pc32 printf]" or "[target verify+27]"); a
// RIP-relative operand with no relocation is resolved against the symbol
// table.  The block hash is the SHA-256 of this canonical byte stream, so a
// block whose only change is its location hashes identically.
package compare

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"bbdiff/internal/cfg"
	"bbdiff/internal/disasm"
	"bbdiff/internal/elfscan"
	"bbdiff/internal/reloc"
)

// SymbolIndex resolves virtual addresses to a stable symbolic name plus
// offset.  It prefers the smallest containing symbol, then falls back to the
// containing section so that unnamed data references remain comparable.
type SymbolIndex struct {
	symbols  []span
	sections []span
}

type span struct {
	start uint64
	end   uint64
	name  string
}

// NewSymbolIndex builds a resolver from a parsed file.
func NewSymbolIndex(f *elfscan.File) *SymbolIndex {
	idx := &SymbolIndex{}
	for i := range f.Symbols {
		s := &f.Symbols[i]
		if !s.Defined || s.OutOfBounds || s.Name == "" {
			continue
		}
		if int(s.SectionIndex) >= len(f.Sections) {
			continue
		}
		sec := f.Sections[s.SectionIndex]
		end := s.Value + s.Size
		if s.Size == 0 {
			// Zero-sized symbols match exactly; give them a one-byte span so
			// they remain candidates without swallowing neighbours.
			end = s.Value + 1
		}
		if end > sec.Addr+sec.Size {
			end = sec.Addr + sec.Size
		}
		if end <= s.Value {
			continue
		}
		idx.symbols = append(idx.symbols, span{start: s.Value, end: end, name: s.Name})
	}
	for i := range f.Sections {
		sec := &f.Sections[i]
		if sec.Name == "" || sec.Size == 0 || sec.Addr == 0 {
			continue
		}
		idx.sections = append(idx.sections, span{start: sec.Addr, end: sec.Addr + sec.Size, name: sec.Name})
	}
	sort.Slice(idx.symbols, func(i, j int) bool {
		a, b := idx.symbols[i], idx.symbols[j]
		if a.start != b.start {
			return a.start < b.start
		}
		if a.end-a.start != b.end-b.start {
			return a.end-a.start < b.end-b.start
		}
		return a.name < b.name
	})
	sort.Slice(idx.sections, func(i, j int) bool {
		if idx.sections[i].start != idx.sections[j].start {
			return idx.sections[i].start < idx.sections[j].start
		}
		return idx.sections[i].name < idx.sections[j].name
	})
	return idx
}

// Resolve returns the symbolic name containing addr and the offset from it.
func (s *SymbolIndex) Resolve(addr uint64) (name string, off int64, ok bool) {
	if s == nil {
		return "", 0, false
	}
	var best *span
	for i := range s.symbols {
		sp := &s.symbols[i]
		if addr < sp.start || addr >= sp.end {
			continue
		}
		if best == nil || sp.end-sp.start < best.end-best.start ||
			(sp.end-sp.start == best.end-best.start && sp.name < best.name) {
			best = sp
		}
	}
	if best != nil {
		return best.name, int64(addr - best.start), true
	}
	for i := range s.sections {
		sp := &s.sections[i]
		if addr < sp.start || addr >= sp.end {
			continue
		}
		if best == nil || sp.name < best.name {
			best = sp
		}
	}
	if best != nil {
		return best.name, int64(addr - best.start), true
	}
	return "", 0, false
}

// CanonInsn is the canonical form of one instruction.
type CanonInsn struct {
	// Encoding is the raw instruction bytes with every normalised field
	// zeroed.  It never aliases the input buffer.
	Encoding []byte
	// Tokens are symbolic replacements for normalised fields, in address
	// order.  They keep two otherwise identical encodings distinguishable.
	Tokens []string
	// Text is the human-readable canonical form used in reports.
	Text string
	// Unresolved lists relocations whose address-dependent bytes could not be
	// normalised.  Those bytes stay in Encoding.
	Unresolved []reloc.Reference
	// LooseEncoding is Encoding with unresolved fields zeroed as well.  Two
	// blocks with equal Loose hashes but different Strict hashes differ only
	// by an address, so their comparison is reported as uncertain.
	LooseEncoding []byte
}

// CanonBlock is the canonical form of a basic block.
type CanonBlock struct {
	Block *cfg.Block
	Insns []CanonInsn
	// Strict is SHA-256 over the canonical instruction stream.
	Strict string
	// Loose is SHA-256 with unresolved address fields blanked too.
	Loose string
	// Unresolved counts unresolved relocations in the block.
	Unresolved int
	// Shapes are operand-shape descriptors used by fuzzy matching; immediate
	// and displacement values are excluded.
	Shapes []string
}

// CanonicalizeFunction canonicalises every block of a function.
func CanonicalizeFunction(blocks []*cfg.Block, relocs reloc.Result, syms *SymbolIndex) []*CanonBlock {
	out := make([]*CanonBlock, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, CanonicalizeBlock(b, relocs, syms))
	}
	return out
}

// CanonicalizeBlock canonicalises one block.
func CanonicalizeBlock(b *cfg.Block, relocs reloc.Result, syms *SymbolIndex) *CanonBlock {
	cb := &CanonBlock{Block: b}
	strict := make([]byte, 0, 256)
	loose := make([]byte, 0, 256)
	for i := range b.Instructions {
		ins := b.Instructions[i]
		if ins.Invalid {
			ci := CanonInsn{Text: fmt.Sprintf("!invalid @0x%x", ins.Address)}
			cb.Insns = append(cb.Insns, ci)
			cb.Shapes = append(cb.Shapes, "!invalid")
			// Strict and loose both include a marker so two invalid blocks are
			// not silently considered equal to an empty block.
			strict = append(strict, 0xff)
			loose = append(loose, 0xff)
			continue
		}
		ci := canonicalInstruction(ins, relocs, syms)
		cb.Insns = append(cb.Insns, ci)
		cb.Shapes = append(cb.Shapes, shape(ins))
		strict = appendInsn(strict, ci.Encoding, ci.Tokens)
		loose = appendInsn(loose, ci.LooseEncoding, ci.Tokens)
		cb.Unresolved += len(ci.Unresolved)
	}
	cb.Strict = hashBytes(strict)
	cb.Loose = hashBytes(loose)
	return cb
}

func appendInsn(dst, enc []byte, tokens []string) []byte {
	dst = binary.AppendUvarint(dst, uint64(len(enc)))
	dst = append(dst, enc...)
	dst = binary.AppendUvarint(dst, uint64(len(tokens)))
	for _, tok := range tokens {
		dst = binary.AppendUvarint(dst, uint64(len(tok)))
		dst = append(dst, tok...)
	}
	return dst
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// field is one relocation site inside an instruction.
type field struct {
	off   uint8
	size  uint8
	ref   reloc.Reference
	isImm bool
}

func canonicalInstruction(ins cfg.Instruction, relocs reloc.Result, syms *SymbolIndex) CanonInsn {
	ci := CanonInsn{Encoding: append([]byte(nil), ins.Bytes...)}
	loose := append([]byte(nil), ins.Bytes...)
	var texts []string

	fields := relocatedFields(ins, relocs)
	unresolvedField := false
	for _, f := range fields {
		if f.ref.Unresolved() {
			unresolvedField = true
			ci.Unresolved = append(ci.Unresolved, f.ref)
			loose = blank(loose, f.off, f.size)
			texts = append(texts, f.ref.Reason)
			continue
		}
		ci.Encoding = blank(ci.Encoding, f.off, f.size)
		loose = blank(loose, f.off, f.size)
		tok := relocToken(ins, f, syms)
		ci.Tokens = append(ci.Tokens, tok)
		texts = append(texts, tok)
	}

	// Address-dependent operands without a relocation record.
	if len(fields) == 0 {
		if tok, off, size, ok := resolveUnrelocated(ins, syms); ok {
			ci.Encoding = blank(ci.Encoding, off, size)
			loose = blank(loose, off, size)
			ci.Tokens = append(ci.Tokens, tok)
			texts = append(texts, tok)
		}
	}

	if len(texts) > 0 && !unresolvedField {
		ci.Text = ins.Mnemonic
		if ins.OpStr != "" {
			ci.Text += " " + ins.OpStr
		}
		ci.Text += "  ; " + strings.Join(texts, " ")
	} else if unresolvedField {
		ci.Text = fmt.Sprintf("%s %s  ; unresolved address", ins.Mnemonic, ins.OpStr)
	} else {
		ci.Text = ins.Mnemonic
		if ins.OpStr != "" {
			ci.Text += " " + ins.OpStr
		}
	}
	ci.LooseEncoding = loose
	return ci
}

func blank(b []byte, off, size uint8) []byte {
	start := int(off)
	end := start + int(size)
	if size == 0 || start < 0 || end > len(b) {
		return b
	}
	for i := start; i < end; i++ {
		b[i] = 0
	}
	return b
}

// relocatedFields returns the relocation sites inside this instruction.  A
// relocation at the displacement field takes precedence over one at the
// immediate field (an instruction has at most one of each in practice).
func relocatedFields(ins cfg.Instruction, relocs reloc.Result) []field {
	var out []field
	if ins.ImmSize > 0 && uint32(ins.ImmSize) <= uint32(ins.Size) {
		if r, ok := relocs.Lookup(ins.Address + uint64(ins.ImmOff)); ok {
			out = append(out, field{off: ins.ImmOff, size: fieldWidth(ins.ImmSize, r), ref: r, isImm: true})
		}
	}
	if ins.DispSize > 0 && uint32(ins.DispSize) <= uint32(ins.Size) {
		if r, ok := relocs.Lookup(ins.Address + uint64(ins.DispOff)); ok {
			out = append(out, field{off: ins.DispOff, size: fieldWidth(ins.DispSize, r), ref: r})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].off < out[j].off })
	return out
}

func fieldWidth(decoded uint8, r reloc.Reference) uint8 {
	if r.FieldSize != 0 && r.FieldSize < decoded {
		return r.FieldSize
	}
	return decoded
}

// relocToken renders a normalised relocation as a symbolic token.  For
// PC-relative fields the effective offset is addend + field size: x86 encodes
// the displacement relative to the end of the instruction, so a zero offset
// (the common case) means "the symbol itself".
func relocToken(ins cfg.Instruction, f field, syms *SymbolIndex) string {
	ref := f.ref
	off := ref.Addend
	if ref.PCRelative {
		// PC-relative relocations are resolved against the field address, while
		// the effective target is relative to the end of the instruction.
		// Adding the field width normalises the addend to "offset from the
		// symbol", so a zero offset means the symbol itself.
		off += int64(f.size)
	}

	if ins.Call || ins.Jump {
		name := ref.Symbol
		if name == "" {
			if target, ok := ins.DirectTarget(); ok {
				if n, delta, found := syms.Resolve(target); found {
					name, off = n, delta
				}
			}
		}
		if name != "" {
			return fmt.Sprintf("[target %s%+d]", name, off)
		}
	}
	switch {
	case ref.Kind == reloc.KindAbsolute:
		return fmt.Sprintf("[abs %s%+d]", ref.Symbol, off)
	case ref.Kind == reloc.KindPLT && f.isImm:
		return fmt.Sprintf("[plt %s%+d]", ref.Symbol, off)
	case ref.Kind == reloc.KindPLT:
		return fmt.Sprintf("[got %s%+d]", ref.Symbol, off)
	case f.isImm:
		return fmt.Sprintf("[imm %s%+d]", ref.Symbol, off)
	default:
		return fmt.Sprintf("[pc32 %s%+d]", ref.Symbol, off)
	}
}

// resolveUnrelocated handles address operands that carry no relocation record:
// RIP-relative memory operands and direct branches/calls.  The branch case
// covers local labels too: the target resolves to the containing function with
// an offset that is invariant when the function moves.
func resolveUnrelocated(ins cfg.Instruction, syms *SymbolIndex) (token string, off, size uint8, ok bool) {
	if syms == nil {
		return "", 0, 0, false
	}
	if (ins.Call || ins.Jump) && ins.ImmSize > 0 {
		if target, has := ins.DirectTarget(); has {
			if name, delta, found := syms.Resolve(target); found {
				return fmt.Sprintf("[target %s%+d]", name, delta), ins.ImmOff, ins.ImmSize, true
			}
		}
	}
	if ins.DispSize > 0 && uint32(ins.DispSize) <= uint32(ins.Size) {
		for _, op := range ins.Operands {
			if op.Type != disasm.OperandMemory || op.Mem.BaseName != "rip" {
				continue
			}
			target := int64(ins.Address) + int64(ins.Size) + op.Mem.Disp
			if target < 0 {
				continue
			}
			if name, delta, found := syms.Resolve(uint64(target)); found {
				return fmt.Sprintf("[rip %s%+d]", name, delta), ins.DispOff, ins.DispSize, true
			}
		}
	}
	return "", 0, 0, false
}

// shape describes an instruction without any absolute or displacement value,
// for fuzzy matching.  Immediates that plausibly carry data (not branch
// targets) are reduced to their size, so a constant change still shows up as a
// change in the encodings but not as a different shape.
func shape(ins cfg.Instruction) string {
	var b strings.Builder
	b.WriteString(ins.Mnemonic)
	for _, op := range ins.Operands {
		switch op.Type {
		case disasm.OperandRegister:
			b.WriteString(" r:" + op.RegName)
		case disasm.OperandImmediate:
			if ins.Call || ins.Jump {
				b.WriteString(" i:target")
			} else {
				b.WriteString(" i")
			}
		case disasm.OperandMemory:
			b.WriteString(" m:" + op.Mem.BaseName + "+" + op.Mem.IndexName)
			b.WriteString(fmt.Sprintf("*%d", op.Mem.Scale))
		}
	}
	return b.String()
}
