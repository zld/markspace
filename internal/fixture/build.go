// Package fixture generates the local ELF samples used by bbdiff's demo and
// tests.
//
// The generated files are synthetic firmware-like ET_EXEC images for x86-64
// with a symbol table (including an alias), PC-relative relocations, branchy
// functions, and a small fixed set of intentional differences between the
// "old" and "new" builds.  The same variant always produces byte-identical
// output, which is what lets the demo reproduce the same summary and block
// hashes on every run.
package fixture

import (
	"debug/elf"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	textVAddr   = 0x401000
	rodataVAddr = 0x402000
	textFileOff = 0x1000
	funcAlign   = 0x10
	pageAlign   = 0x1000

	rX8664PC32  = 2
	rX8664PLT32 = 4
)

// Variant selects which build to emit.
type Variant int

const (
	// Old is the pre-upgrade build.  It contains legacy_path and the original
	// immediate constants.
	Old Variant = iota
	// New is the post-upgrade build.  It adds audit_gate, bumps the immediates
	// in verify, and drops legacy_path.
	New
	// Corrupt is a variant of New whose relocation that targets verify has an
	// unknown type.  The address-dependent bytes survive unnormalised, so
	// bbdiff reports the affected blocks as uncertain instead of changed.
	// It exists for demonstrating and testing the degraded path.
	Corrupt
)

func (v Variant) String() string {
	switch v {
	case Old:
		return "old"
	case New:
		return "new"
	case Corrupt:
		return "corrupt"
	default:
		return fmt.Sprintf("variant(%d)", int(v))
	}
}

// Write produces a sample for the requested variant at path.
//
// The output path must not contain ".." components: fixtures are disposable
// local artifacts and bbdiff refuses to write them through a traversal path.
// The write itself is atomic (sibling temp file plus rename), so a failed run
// never leaves a partial ELF behind, and the temporary file is always removed.
func Write(path string, variant Variant) error {
	if err := ValidateOutputPath(path); err != nil {
		return err
	}
	data, err := Build(variant)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("fixture: create output directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("fixture: create temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("fixture: write temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("fixture: sync temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("fixture: close temporary file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("fixture: install %s: %w", filepath.Base(path), err)
	}
	return nil
}

// ValidateOutputPath rejects empty paths, directory paths, and paths that
// traverse out of their parent with ".." components.
func ValidateOutputPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("fixture: empty output path")
	}
	if strings.HasSuffix(path, string(os.PathSeparator)) {
		return fmt.Errorf("fixture: output path %q is a directory", path)
	}
	clean := filepath.Clean(path)
	if clean == "." || clean == ".." || clean == string(os.PathSeparator) {
		return fmt.Errorf("fixture: invalid output path %q", path)
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return fmt.Errorf("fixture: refusing output path %q: path traversal is not allowed", path)
		}
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return fmt.Errorf("fixture: output path %q is a directory", path)
	}
	return nil
}

// Build returns the bytes of the requested variant.
func Build(variant Variant) ([]byte, error) {
	if variant != Old && variant != New && variant != Corrupt {
		return nil, fmt.Errorf("fixture: unsupported variant %d", int(variant))
	}
	// Corrupt shares New's layout and code; only one relocation record is
	// rewritten at the end.
	corrupt := variant == Corrupt
	if corrupt {
		variant = New
	}
	asm := newAssembler()

	// init sits at the same address in both builds, so its call to verify
	// exercises relocation normalisation rather than a uniform address shift.
	asm.function([]string{"init"})
	asm.label("entry")
	asm.emit(0x55)               // push rbp
	asm.emit(0x48, 0x89, 0xe5)   // mov rbp, rsp
	asm.emit(0x83, 0xff, 0x02)   // cmp edi, 2
	asm.condJump(0x74, "report") // je report
	asm.emit(0x31, 0xc0)         // xor eax, eax
	asm.jmp("report")
	asm.call("verify")
	asm.label("report")
	asm.emit(0x5d) // pop rbp
	asm.emit(0xc3) // ret

	if variant == New {
		// audit_gate is inserted before verify: every following function
		// shifts by one aligned slot without changing its instruction bytes.
		asm.function([]string{"audit_gate"})
		asm.emit(0x55, 0x48, 0x89, 0xe5) // push rbp; mov rbp, rsp
		asm.emit(0x31, 0xc0)             // xor eax, eax
		asm.emit(0x5d, 0xc3)             // pop rbp; ret
	}

	bump := uint8(0)
	if variant == New {
		bump = 1
	}

	asm.function([]string{"verify", "verify_alias"})
	asm.emit(0x55, 0x48, 0x89, 0xe5) // push rbp; mov rbp, rsp
	asm.emit(0x85, 0xf6)             // test esi, esi
	asm.condJump(0x74, "check")      // je check
	asm.movEAXImm32(uint32(0x2a) + uint32(bump))
	asm.jmp("fail")
	asm.label("fail")
	asm.leaRIP("rdi", "msg_err") // lea rdi, [rip+msg_err]
	asm.call("report_error")
	asm.label("check")
	asm.emit(0x83, 0xfa, uint8(7)+bump) // cmp edx, 7 / 8
	asm.condJump(0x75, "deny")          // jne deny
	asm.movEAXImm32(1)
	asm.jmp("done")
	asm.label("deny")
	asm.movEAXImm32(2)
	asm.label("done")
	asm.emit(0x5d, 0xc3) // pop rbp; ret

	asm.function([]string{"report_error"})
	asm.emit(0x55, 0x48, 0x89, 0xe5) // push rbp; mov rbp, rsp
	asm.movESIImm32(1)
	asm.emit(0x5d, 0xc3)

	if variant == Old {
		asm.function([]string{"legacy_path"})
		asm.emit(0x55, 0x48, 0x89, 0xe5)
		asm.movEAXImm32(0xdead)
		asm.emit(0x5d, 0xc3)
	}

	asm.blob("msg_ok", []byte("verified\x00"))
	asm.blob("msg_err", []byte("rejected\x00"))

	img, err := asm.image()
	if err != nil {
		return nil, err
	}
	if corrupt {
		if err := corruptFirstCodeRelocation(img); err != nil {
			return nil, err
		}
	}
	return img, nil
}

// ---------------------------------------------------------------------------
// Minimal deterministic assembler

type fixKind int

const (
	fixRel8 fixKind = iota
	fixRel32
	fixRIP32
)

type fixup struct {
	kind     fixKind
	fieldOff int    // field offset within the function body
	instrEnd int    // offset just past the instruction within the function body
	target   string // local label or symbol name
	reloc    bool
	relocTyp uint32
}

type asmFunction struct {
	names  []string
	code   []byte
	labels map[string]int
	fixups []fixup
	start  int // assigned by layout
}

type dataBlob struct {
	name string
	data []byte
	addr uint64
}

type assembler struct {
	funcs []*asmFunction
	cur   *asmFunction
	blobs []dataBlob
}

func newAssembler() *assembler { return &assembler{} }

func (a *assembler) function(names []string) {
	if len(names) == 0 {
		panic("fixture: function without a name")
	}
	fn := &asmFunction{names: names, labels: make(map[string]int), start: -1}
	a.funcs = append(a.funcs, fn)
	a.cur = fn
}

func (a *assembler) blob(name string, payload []byte) {
	a.blobs = append(a.blobs, dataBlob{name: name, data: payload})
}

func (a *assembler) label(name string) {
	if a.cur == nil {
		panic("fixture: label before function")
	}
	a.cur.labels[name] = len(a.cur.code)
}

func (a *assembler) emit(b ...byte) {
	if a.cur == nil {
		panic("fixture: instruction before function")
	}
	a.cur.code = append(a.cur.code, b...)
}

func (a *assembler) movEAXImm32(v uint32) {
	a.emit(0xb8)
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], v)
	a.emit(buf[:]...)
}

func (a *assembler) movESIImm32(v uint32) {
	a.emit(0xbe)
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], v)
	a.emit(buf[:]...)
}

func (a *assembler) condJump(opcode byte, label string) {
	a.emit(opcode, 0x00)
	a.cur.fixups = append(a.cur.fixups, fixup{
		kind:     fixRel8,
		fieldOff: len(a.cur.code) - 1,
		instrEnd: len(a.cur.code),
		target:   label,
	})
}

func (a *assembler) jmp(label string) {
	a.emit(0xeb, 0x00)
	a.cur.fixups = append(a.cur.fixups, fixup{
		kind:     fixRel8,
		fieldOff: len(a.cur.code) - 1,
		instrEnd: len(a.cur.code),
		target:   label,
	})
}

// call emits "call rel32" and records a PLT32 relocation so that the address
// remains normalisable across builds.
func (a *assembler) call(symbol string) {
	a.emit(0xe8, 0, 0, 0, 0)
	a.cur.fixups = append(a.cur.fixups, fixup{
		kind:     fixRel32,
		fieldOff: len(a.cur.code) - 4,
		instrEnd: len(a.cur.code),
		target:   symbol,
		reloc:    true,
		relocTyp: rX8664PLT32,
	})
}

// leaRIP emits "lea rdi, [rip+disp32]" with an R_X86_64_PC32 relocation.
func (a *assembler) leaRIP(reg, symbol string) {
	if reg != "rdi" {
		panic("fixture: unsupported leaRIP register " + reg)
	}
	a.emit(0x48, 0x8d, 0x3d, 0, 0, 0, 0)
	a.cur.fixups = append(a.cur.fixups, fixup{
		kind:     fixRIP32,
		fieldOff: len(a.cur.code) - 4,
		instrEnd: len(a.cur.code),
		target:   symbol,
		reloc:    true,
		relocTyp: rX8664PC32,
	})
}

func (a *assembler) layout() error {
	off := uint64(0)
	for _, fn := range a.funcs {
		if len(fn.code) == 0 {
			return fmt.Errorf("fixture: function %q has no code", fn.names[0])
		}
		off = alignUp(off, funcAlign)
		fn.start = int(off)
		off += uint64(len(fn.code))
	}
	return nil
}

type relocRecord struct {
	addr   uint64
	symbol string
	typ    uint32
	addend int64
}

func (a *assembler) image() ([]byte, error) {
	if err := a.layout(); err != nil {
		return nil, err
	}

	textEnd := uint64(0)
	for _, fn := range a.funcs {
		end := uint64(fn.start + len(fn.code))
		if end > textEnd {
			textEnd = end
		}
	}
	textSize := alignUp(textEnd, funcAlign)

	symbolAddr := make(map[string]uint64)
	for _, fn := range a.funcs {
		for _, name := range fn.names {
			symbolAddr[name] = textVAddr + uint64(fn.start)
		}
	}
	var rodataSize uint64
	for i := range a.blobs {
		a.blobs[i].addr = rodataVAddr + rodataSize
		rodataSize += uint64(len(a.blobs[i].data))
		symbolAddr[a.blobs[i].name] = a.blobs[i].addr
	}
	rodataSize = alignUp(rodataSize, 8)

	// Resolve fixups and collect relocation records.
	var relocs []relocRecord
	for _, fn := range a.funcs {
		for _, fx := range fn.fixups {
			var target uint64
			if local, ok := fn.labels[fx.target]; ok {
				target = textVAddr + uint64(fn.start+local)
			} else if addr, ok := symbolAddr[fx.target]; ok {
				target = addr
			} else {
				return nil, fmt.Errorf("fixture: unresolved fixup target %q in %s", fx.target, fn.names[0])
			}
			instrEnd := textVAddr + uint64(fn.start+fx.instrEnd)
			disp := int64(target) - int64(instrEnd)
			switch fx.kind {
			case fixRel8:
				if disp < -128 || disp > 127 {
					return nil, fmt.Errorf("fixture: rel8 out of range in %s (%d)", fn.names[0], disp)
				}
				fn.code[fx.fieldOff] = byte(int8(disp))
			case fixRel32, fixRIP32:
				if disp < -1<<31 || disp >= 1<<31 {
					return nil, fmt.Errorf("fixture: rel32 out of range in %s (%d)", fn.names[0], disp)
				}
				binary.LittleEndian.PutUint32(fn.code[fx.fieldOff:fx.fieldOff+4], uint32(int32(disp)))
			}
			if fx.reloc {
				relocs = append(relocs, relocRecord{
					addr:   textVAddr + uint64(fn.start+fx.fieldOff),
					symbol: fx.target,
					typ:    fx.relocTyp,
					addend: -4,
				})
			}
		}
	}

	// Symbols: null, two section symbols, then globals in fixed order.
	type symDef struct {
		name  string
		value uint64
		size  uint64
		info  uint8
		shndx uint16
	}
	const (
		shText   = 1
		shRodata = 2
	)
	syms := []symDef{
		{},
		{value: textVAddr, info: byte(elf.STT_SECTION), shndx: shText},
		{value: rodataVAddr, info: byte(elf.STT_SECTION), shndx: shRodata},
	}
	firstGlobal := len(syms)

	order := []string{
		"init", "audit_gate", "verify", "verify_alias", "report_error", "legacy_path",
		"msg_ok", "msg_err",
	}
	for _, name := range order {
		addr, ok := symbolAddr[name]
		if !ok {
			continue
		}
		sym := symDef{
			name:  name,
			value: addr,
			info:  byte(elf.STB_GLOBAL)<<4 | byte(elf.STT_OBJECT),
			shndx: shRodata,
		}
		for _, fn := range a.funcs {
			if contains(fn.names, name) {
				sym.info = byte(elf.STB_GLOBAL)<<4 | byte(elf.STT_FUNC)
				sym.size = uint64(len(fn.code))
				sym.shndx = shText
				break
			}
		}
		if sym.shndx == shRodata {
			for _, b := range a.blobs {
				if b.name == name {
					sym.size = uint64(len(b.data))
				}
			}
		}
		syms = append(syms, sym)
	}

	// Build string tables.
	strtab := []byte{0}
	strOff := make(map[string]uint32)
	for _, s := range syms {
		if s.name == "" {
			continue
		}
		if _, ok := strOff[s.name]; ok {
			continue
		}
		strOff[s.name] = uint32(len(strtab))
		strtab = append(strtab, s.name...)
		strtab = append(strtab, 0)
	}

	sectionNames := []string{".text", ".rodata", ".rela.text", ".symtab", ".strtab", ".shstrtab"}
	shstrtab := []byte{0}
	shOff := make(map[string]uint32)
	for _, name := range sectionNames {
		shOff[name] = uint32(len(shstrtab))
		shstrtab = append(shstrtab, name...)
		shstrtab = append(shstrtab, 0)
	}

	symtab := make([]byte, 0, len(syms)*24)
	symIndex := make(map[string]uint32)
	for i, s := range syms {
		if s.name != "" {
			symIndex[s.name] = uint32(i)
		}
		var entry [24]byte
		binary.LittleEndian.PutUint32(entry[0:4], strOff[s.name])
		entry[4] = s.info
		binary.LittleEndian.PutUint16(entry[6:8], s.shndx)
		binary.LittleEndian.PutUint64(entry[8:16], s.value)
		binary.LittleEndian.PutUint64(entry[16:24], s.size)
		symtab = append(symtab, entry[:]...)
	}

	rela := make([]byte, 0, len(relocs)*24)
	for _, r := range relocs {
		idx, ok := symIndex[r.symbol]
		if !ok {
			return nil, fmt.Errorf("fixture: relocation references unknown symbol %q", r.symbol)
		}
		var entry [24]byte
		binary.LittleEndian.PutUint64(entry[0:8], r.addr)
		binary.LittleEndian.PutUint64(entry[8:16], uint64(idx)<<32|uint64(r.typ))
		binary.LittleEndian.PutUint64(entry[16:24], uint64(r.addend))
		rela = append(rela, entry[:]...)
	}

	// Assemble sections.  Inter-function padding is int3 (0xcc), typical for
	// firmware images.
	text := make([]byte, textSize)
	for i := range text {
		text[i] = 0xcc
	}
	for _, fn := range a.funcs {
		copy(text[fn.start:], fn.code)
	}
	rodata := make([]byte, rodataSize)
	for _, b := range a.blobs {
		copy(rodata[b.addr-rodataVAddr:], b.data)
	}

	const (
		ehsize    = 64
		phentsize = 56
		phnum     = 2
		shentsize = 64
		shnum     = 7
	)
	phoff := uint64(ehsize)
	rodataOff := alignUp(textFileOff+textSize, pageAlign)
	relaOff := alignUp(rodataOff+rodataSize, 8)
	symtabOff := alignUp(relaOff+uint64(len(rela)), 8)
	strtabOff := symtabOff + uint64(len(symtab))
	shstrOff := strtabOff + uint64(len(strtab))
	shoff := alignUp(shstrOff+uint64(len(shstrtab)), 8)
	total := shoff + shnum*shentsize
	if total > 1<<31 {
		return nil, errors.New("fixture: image exceeds 2 GiB")
	}
	img := make([]byte, int(total))

	// ELF header.
	copy(img[0:4], "\x7fELF")
	img[4] = byte(elf.ELFCLASS64)
	img[5] = byte(elf.ELFDATA2LSB)
	img[6] = byte(elf.EV_CURRENT)
	binary.LittleEndian.PutUint16(img[16:18], uint16(elf.ET_EXEC))
	binary.LittleEndian.PutUint16(img[18:20], uint16(elf.EM_X86_64))
	binary.LittleEndian.PutUint32(img[20:24], uint32(elf.EV_CURRENT))
	binary.LittleEndian.PutUint64(img[24:32], textVAddr) // entry: init
	binary.LittleEndian.PutUint64(img[32:40], phoff)
	binary.LittleEndian.PutUint64(img[40:48], shoff)
	binary.LittleEndian.PutUint16(img[52:54], ehsize)
	binary.LittleEndian.PutUint16(img[54:56], phentsize)
	binary.LittleEndian.PutUint16(img[56:58], phnum)
	binary.LittleEndian.PutUint16(img[58:60], shentsize)
	binary.LittleEndian.PutUint16(img[60:62], shnum)
	binary.LittleEndian.PutUint16(img[62:64], 6) // .shstrtab index

	writePhdr(img[int(phoff):], 1, 5, textFileOff, textVAddr, textSize, textSize, pageAlign)
	writePhdr(img[int(phoff)+phentsize:], 1, 4, rodataOff, rodataVAddr, rodataSize, rodataSize, pageAlign)

	copy(img[textFileOff:], text)
	copy(img[rodataOff:], rodata)
	copy(img[relaOff:], rela)
	copy(img[symtabOff:], symtab)
	copy(img[strtabOff:], strtab)
	copy(img[shstrOff:], shstrtab)

	// Section headers.
	type shdr struct {
		name    uint32
		typ     uint64
		flags   uint64
		addr    uint64
		off     uint64
		size    uint64
		link    uint64
		info    uint64
		align   uint64
		entsize uint64
	}
	headers := []shdr{
		{},
		{name: shOff[".text"], typ: uint64(elf.SHT_PROGBITS), flags: uint64(elf.SHF_ALLOC | elf.SHF_EXECINSTR), addr: textVAddr, off: textFileOff, size: textSize, align: 16},
		{name: shOff[".rodata"], typ: uint64(elf.SHT_PROGBITS), flags: uint64(elf.SHF_ALLOC), addr: rodataVAddr, off: rodataOff, size: rodataSize, align: 8},
		{name: shOff[".rela.text"], typ: uint64(elf.SHT_RELA), off: relaOff, size: uint64(len(rela)), link: 4, info: 1, align: 8, entsize: 24},
		{name: shOff[".symtab"], typ: uint64(elf.SHT_SYMTAB), off: symtabOff, size: uint64(len(symtab)), link: 5, info: uint64(firstGlobal), align: 8, entsize: 24},
		{name: shOff[".strtab"], typ: uint64(elf.SHT_STRTAB), off: strtabOff, size: uint64(len(strtab)), align: 1},
		{name: shOff[".shstrtab"], typ: uint64(elf.SHT_STRTAB), off: shstrOff, size: uint64(len(shstrtab)), align: 1},
	}
	for i, h := range headers {
		buf := img[int(shoff)+i*shentsize:]
		binary.LittleEndian.PutUint32(buf[0:4], h.name)
		binary.LittleEndian.PutUint32(buf[4:8], uint32(h.typ))
		binary.LittleEndian.PutUint64(buf[8:16], h.flags)
		binary.LittleEndian.PutUint64(buf[16:24], h.addr)
		binary.LittleEndian.PutUint64(buf[24:32], h.off)
		binary.LittleEndian.PutUint64(buf[32:40], h.size)
		binary.LittleEndian.PutUint32(buf[40:44], uint32(h.link))
		binary.LittleEndian.PutUint32(buf[44:48], uint32(h.info))
		binary.LittleEndian.PutUint64(buf[48:56], h.align)
		binary.LittleEndian.PutUint64(buf[56:64], h.entsize)
	}
	return img, nil
}

func writePhdr(dst []byte, typ, flags uint32, off, vaddr, filesz, memsz, align uint64) {
	binary.LittleEndian.PutUint32(dst[0:4], typ)
	binary.LittleEndian.PutUint32(dst[4:8], flags)
	binary.LittleEndian.PutUint64(dst[8:16], off)
	binary.LittleEndian.PutUint64(dst[16:24], vaddr)
	binary.LittleEndian.PutUint64(dst[24:32], vaddr)
	binary.LittleEndian.PutUint64(dst[32:40], filesz)
	binary.LittleEndian.PutUint64(dst[40:48], memsz)
	binary.LittleEndian.PutUint64(dst[48:56], align)
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func alignUp(v, align uint64) uint64 {
	if align <= 1 {
		return v
	}
	return (v + align - 1) &^ (align - 1)
}

// corruptFirstCodeRelocation rewrites the first relocation that targets the
// text section so its type is unknown to bbdiff (999).  The ELF stays
// structurally valid; only the normalisation of that field fails, which is the
// degradation the uncertain path is meant to exercise.
func corruptFirstCodeRelocation(img []byte) error {
	const shoffOff = 40
	shoff := binary.LittleEndian.Uint64(img[shoffOff : shoffOff+8])
	shnum := binary.LittleEndian.Uint16(img[60:62])
	for i := 0; i < int(shnum); i++ {
		hdr := img[int(shoff)+i*64:]
		typ := binary.LittleEndian.Uint32(hdr[4:8])
		if typ != uint32(elf.SHT_RELA) {
			continue
		}
		info := binary.LittleEndian.Uint32(hdr[44:48])
		if int(info) >= len(img) {
			continue
		}
		targetHdr := img[int(shoff)+int(info)*64:]
		targetFlags := binary.LittleEndian.Uint64(targetHdr[8:16])
		if targetFlags&uint64(elf.SHF_EXECINSTR) == 0 {
			continue
		}
		off := binary.LittleEndian.Uint64(hdr[24:32])
		size := binary.LittleEndian.Uint64(hdr[32:40])
		if size < 24 || off+24 > uint64(len(img)) {
			return errors.New("fixture: relocation section is too small to corrupt")
		}
		rInfo := binary.LittleEndian.Uint64(img[off+8 : off+16])
		symbol := rInfo >> 32
		binary.LittleEndian.PutUint64(img[off+8:off+16], symbol<<32|999)
		return nil
	}
	return errors.New("fixture: no relocation section targets executable code")
}
