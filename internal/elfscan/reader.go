// Package elfscan reads the parts of an ELF64 little-endian x86-64 file that
// bbdiff needs: section table, symbol tables, and relocation records.
//
// The reader is deliberately strict about file boundaries and table indexes:
// a malformed sample must produce a diagnostic or a clear error, never a panic
// or an out-of-range read.  Standard library debug/elf does the structural
// parsing; symbol and relocation records are decoded here so that every index
// and string offset can be validated explicitly.
package elfscan

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ReadError marks a structural problem with an input file.  Callers use it to
// distinguish "this is not a usable sample" from analysis failures.
type ReadError struct {
	Op   string
	Path string
	Err  error
}

func (e *ReadError) Error() string {
	if e.Path == "" {
		return fmt.Sprintf("elfscan: %s: %v", e.Op, e.Err)
	}
	return fmt.Sprintf("elfscan: %s %s: %v", e.Op, filepath.Base(e.Path), e.Err)
}

func (e *ReadError) Unwrap() error { return e.Err }

func readError(path, op string, err error) error {
	return &ReadError{Op: op, Path: path, Err: err}
}

// Severity classifies a non-fatal diagnostic.
type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// Diagnostic is a deterministic, non-fatal observation about a sample.
type Diagnostic struct {
	Severity Severity `json:"severity"`
	Code     string   `json:"code"`
	Message  string   `json:"message"`
}

func (d Diagnostic) String() string {
	return fmt.Sprintf("%s: %s: %s", d.Severity, d.Code, d.Message)
}

// Section is a normalized section table entry.
type Section struct {
	Index      int
	Name       string
	Type       elf.SectionType
	Flags      elf.SectionFlag
	Addr       uint64
	Offset     uint64
	Size       uint64
	EntSize    uint64
	Link       uint32
	Info       uint32
	Data       []byte
	FileBacked bool
	Executable bool
}

// Symbol is a decoded ELF64 symbol.
type Symbol struct {
	Table        int
	Index        int
	Name         string
	Value        uint64
	Size         uint64
	Info         uint8
	Other        uint8
	SectionIndex uint16
	Section      string
	Type         elf.SymType
	Bind         elf.SymBind
	Defined      bool
	OutOfBounds  bool
}

// Reloc is a decoded relocation record.  Address is the virtual address the
// relocation applies to (for ET_REL files this is the target section offset).
type Reloc struct {
	Section      int
	Address      uint64
	Type         uint32
	SymbolIndex  int
	SymbolName   string
	SymbolValue  uint64
	SymbolShndx  uint16
	SymbolType   elf.SymType
	Addend       int64
	HasAddend    bool
	Unresolved   string
	SourceOffset uint64
}

// File is a validated ELF sample.
type File struct {
	Path        string
	Size        int64
	Type        elf.Type
	Machine     elf.Machine
	Entry       uint64
	Class       elf.Class
	Data        elf.Data
	Sections    []Section
	Symbols     []Symbol
	Relocs      []Reloc
	Diagnostics []Diagnostic

	// SymbolTables maps a section index of type SHT_SYMTAB/SHT_DYNSYM to the
	// symbols decoded from it, in table order.
	SymbolTables map[int][]Symbol
}

// Options bounds memory and work for hostile or oversized inputs.
type Options struct {
	MaxFileSize    int64
	MaxSectionSize int64
	MaxSymbols     int
	MaxRelocs      int
	MaxSections    int
}

// DefaultOptions returns conservative local-analysis limits.
func DefaultOptions() Options {
	return Options{
		MaxFileSize:    512 << 20,
		MaxSectionSize: 256 << 20,
		MaxSymbols:     1 << 20,
		MaxRelocs:      1 << 20,
		MaxSections:    1 << 16,
	}
}

// Read opens path and returns a validated view of the ELF file.
func Read(path string, opt Options) (*File, error) {
	if path == "" {
		return nil, errors.New("elfscan: empty input path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, readError(path, "read", fmt.Errorf("stat %s: %w", filepath.Base(path), err))
	}
	if !info.Mode().IsRegular() {
		return nil, readError(path, "read", fmt.Errorf("%s is not a regular file", filepath.Base(path)))
	}
	if info.Size() == 0 {
		return nil, readError(path, "read", fmt.Errorf("%s is empty", filepath.Base(path)))
	}
	if opt.MaxFileSize > 0 && info.Size() > opt.MaxFileSize {
		return nil, readError(path, "read", fmt.Errorf("%s is %d bytes, exceeds --max-file-size %d", filepath.Base(path), info.Size(), opt.MaxFileSize))
	}
	if opt.MaxSections <= 0 {
		opt.MaxSections = DefaultOptions().MaxSections
	}
	if opt.MaxSymbols <= 0 {
		opt.MaxSymbols = DefaultOptions().MaxSymbols
	}
	if opt.MaxRelocs <= 0 {
		opt.MaxRelocs = DefaultOptions().MaxRelocs
	}
	if opt.MaxSectionSize <= 0 {
		opt.MaxSectionSize = DefaultOptions().MaxSectionSize
	}

	fh, err := os.Open(path)
	if err != nil {
		return nil, readError(path, "read", fmt.Errorf("open %s: %w", filepath.Base(path), err))
	}
	defer fh.Close()

	ef, err := elf.NewFile(fh)
	if err != nil {
		return nil, readError(path, "read", fmt.Errorf("parse %s: %w", filepath.Base(path), describeELFBError(err)))
	}
	defer ef.Close()

	if err := validateHeader(ef, info.Size()); err != nil {
		return nil, readError(path, "validate", err)
	}

	f := &File{
		Path:         path,
		Size:         info.Size(),
		Type:         ef.Type,
		Machine:      ef.Machine,
		Entry:        ef.Entry,
		Class:        ef.Class,
		Data:         ef.Data,
		SymbolTables: make(map[int][]Symbol),
	}

	if len(ef.Sections) > opt.MaxSections {
		return nil, readError(path, "read", fmt.Errorf("%s has %d sections, exceeds limit %d", filepath.Base(path), len(ef.Sections), opt.MaxSections))
	}

	for i, s := range ef.Sections {
		sec := Section{
			Index:      i,
			Name:       s.Name,
			Type:       s.Type,
			Flags:      s.Flags,
			Addr:       s.Addr,
			Offset:     s.Offset,
			Size:       s.Size,
			EntSize:    s.Entsize,
			Link:       s.Link,
			Info:       s.Info,
			Executable: s.Flags&elf.SHF_EXECINSTR != 0,
		}
		sec.FileBacked = sec.Type != elf.SHT_NULL && sec.Type != elf.SHT_NOBITS
		if err := checkSectionBounds(i, sec, info.Size(), opt, &f.Diagnostics); err != nil {
			return nil, readError(path, "validate", err)
		}
		if sec.FileBacked && sec.Size > 0 {
			if sec.Size > uint64(opt.MaxSectionSize) {
				f.Diagnostics = append(f.Diagnostics, Diagnostic{
					Severity: SeverityWarning,
					Code:     "section-too-large",
					Message:  fmt.Sprintf("section %d (%s) is %d bytes; contents skipped (limit %d)", i, sec.Name, sec.Size, opt.MaxSectionSize),
				})
			} else {
				data, err := s.Data()
				if err != nil {
					return nil, readError(path, "read", fmt.Errorf("read section %d (%s) in %s: %w", i, sec.Name, filepath.Base(path), err))
				}
				sec.Data = data
			}
		}
		f.Sections = append(f.Sections, sec)
	}

	f.Symbols, err = f.readSymbols(ef, opt)
	if err != nil {
		return nil, err
	}
	f.readRelocs(opt)
	f.validateSymbols()
	return f, nil
}

func validateHeader(ef *elf.File, size int64) error {
	if ef.Class != elf.ELFCLASS64 {
		return fmt.Errorf("unsupported ELF class %s: only ELF64 is supported", ef.Class)
	}
	if ef.Data != elf.ELFDATA2LSB {
		return fmt.Errorf("unsupported byte order %s: only little-endian ELF is supported", ef.Data)
	}
	if ef.Machine != elf.EM_X86_64 {
		return fmt.Errorf("unsupported machine %s (%d): only x86-64 (%d) is supported", ef.Machine, ef.Machine, elf.EM_X86_64)
	}
	switch ef.Type {
	case elf.ET_EXEC, elf.ET_DYN, elf.ET_REL:
	default:
		return fmt.Errorf("unsupported ELF type %s: expected ET_EXEC, ET_DYN or ET_REL", ef.Type)
	}
	if ef.Entry > uint64(size)+0x100000000 {
		// Entry points may live above the file size in linked images; only
		// reject values that could never be a virtual address on amd64.
		return fmt.Errorf("implausible entry point 0x%x", ef.Entry)
	}
	return nil
}

func checkSectionBounds(i int, sec Section, fileSize int64, opt Options, diags *[]Diagnostic) error {
	if sec.Type == elf.SHT_NULL {
		return nil
	}
	if sec.Type == elf.SHT_NOBITS {
		return nil // occupies memory, not file bytes
	}
	sec.FileBacked = true
	if sec.Size == 0 {
		return nil
	}
	if sec.Offset > uint64(fileSize) || sec.Size > uint64(fileSize)-sec.Offset {
		return fmt.Errorf("section %d (%s) extends past end of file (offset %#x, size %#x, file size %#x)", i, sec.Name, sec.Offset, sec.Size, fileSize)
	}
	if sec.EntSize > 0 && sec.Size%sec.EntSize != 0 {
		*diags = append(*diags, Diagnostic{
			Severity: SeverityWarning,
			Code:     "section-size-misaligned",
			Message:  fmt.Sprintf("section %d (%s) size %#x is not a multiple of entry size %#x", i, sec.Name, sec.Size, sec.EntSize),
		})
	}
	return nil
}

func (f *File) readSymbols(ef *elf.File, opt Options) ([]Symbol, error) {
	var all []Symbol
	for i, s := range f.Sections {
		if s.Type != elf.SHT_SYMTAB && s.Type != elf.SHT_DYNSYM {
			continue
		}
		if len(s.Data) == 0 {
			f.Diagnostics = append(f.Diagnostics, Diagnostic{
				Severity: SeverityWarning,
				Code:     "empty-symbol-table",
				Message:  fmt.Sprintf("symbol table section %d (%s) has no data", i, s.Name),
			})
			continue
		}
		if int(s.Link) >= len(f.Sections) || s.Link == 0 {
			f.Diagnostics = append(f.Diagnostics, Diagnostic{
				Severity: SeverityWarning,
				Code:     "bad-symbol-strtab",
				Message:  fmt.Sprintf("symbol table section %d (%s) links to invalid string table %d", i, s.Name, s.Link),
			})
			continue
		}
		strtab := f.Sections[s.Link]
		if len(strtab.Data) == 0 && strtab.Size > 0 {
			f.Diagnostics = append(f.Diagnostics, Diagnostic{
				Severity: SeverityWarning,
				Code:     "unreadable-symbol-strtab",
				Message:  fmt.Sprintf("string table section %d (%s) has no readable data", strtab.Index, strtab.Name),
			})
			continue
		}
		entSize := int(s.EntSize)
		if entSize == 0 {
			entSize = 24
		}
		if entSize != 24 {
			f.Diagnostics = append(f.Diagnostics, Diagnostic{
				Severity: SeverityWarning,
				Code:     "unexpected-symbol-entry-size",
				Message:  fmt.Sprintf("symbol table section %d (%s) has entry size %d; expected 24", i, s.Name, entSize),
			})
			continue
		}
		count := len(s.Data) / entSize
		if count > opt.MaxSymbols {
			count = opt.MaxSymbols
			f.Diagnostics = append(f.Diagnostics, Diagnostic{
				Severity: SeverityWarning,
				Code:     "symbol-limit",
				Message:  fmt.Sprintf("symbol table section %d (%s) truncated to %d entries", i, s.Name, count),
			})
		}
		tableSyms := make([]Symbol, 0, count)
		for j := 0; j < count; j++ {
			raw := s.Data[j*entSize : (j+1)*entSize]
			sym := decodeSymbol(i, j, raw, strtab.Data, f.Sections)
			tableSyms = append(tableSyms, sym)
			all = append(all, sym)
			if len(all) >= opt.MaxSymbols {
				f.Diagnostics = append(f.Diagnostics, Diagnostic{
					Severity: SeverityWarning,
					Code:     "symbol-limit",
					Message:  fmt.Sprintf("symbol limit %d reached; remaining tables ignored", opt.MaxSymbols),
				})
				f.SymbolTables[i] = tableSyms
				return all, nil
			}
		}
		f.SymbolTables[i] = tableSyms
	}
	return all, nil
}

func decodeSymbol(table, index int, raw, strtab []byte, sections []Section) Symbol {
	nameOff := binary.LittleEndian.Uint32(raw[0:4])
	info := raw[4]
	other := raw[5]
	shndx := binary.LittleEndian.Uint16(raw[6:8])
	value := binary.LittleEndian.Uint64(raw[8:16])
	size := binary.LittleEndian.Uint64(raw[16:24])

	name := ""
	if uint64(nameOff) < uint64(len(strtab)) {
		end := bytes.IndexByte(strtab[nameOff:], 0)
		if end < 0 {
			end = len(strtab) - int(nameOff)
		}
		name = string(strtab[nameOff : int(nameOff)+end])
	}

	sym := Symbol{
		Table:        table,
		Index:        index,
		Name:         name,
		Value:        value,
		Size:         size,
		Info:         info,
		Other:        other,
		SectionIndex: shndx,
		Type:         elf.SymType(info & 0xf),
		Bind:         elf.SymBind(info >> 4),
	}
	if shndx != uint16(elf.SHN_UNDEF) && int(shndx) < len(sections) {
		sym.Defined = true
		sym.Section = sections[shndx].Name
	}
	return sym
}

// validateSymbols flags symbols whose address range is not contained in their
// declared section.  The symbols are kept; consumers decide whether to skip
// them, so the outcome stays deterministic and inspectable.
func (f *File) validateSymbols() {
	for i := range f.Symbols {
		s := &f.Symbols[i]
		if !s.Defined {
			continue
		}
		if int(s.SectionIndex) >= len(f.Sections) {
			s.OutOfBounds = true
			f.Diagnostics = append(f.Diagnostics, Diagnostic{
				Severity: SeverityWarning,
				Code:     "symbol-section-out-of-range",
				Message:  fmt.Sprintf("symbol %q references invalid section index %d", s.Name, s.SectionIndex),
			})
			continue
		}
		sec := f.Sections[s.SectionIndex]
		if sec.Size == 0 {
			s.OutOfBounds = true
			f.Diagnostics = append(f.Diagnostics, Diagnostic{
				Severity: SeverityWarning,
				Code:     "symbol-in-empty-section",
				Message:  fmt.Sprintf("symbol %q is defined in empty section %d (%s)", s.Name, sec.Index, sec.Name),
			})
			continue
		}
		end := sec.Addr + sec.Size
		if s.Value < sec.Addr || s.Value >= end {
			s.OutOfBounds = true
			f.Diagnostics = append(f.Diagnostics, Diagnostic{
				Severity: SeverityWarning,
				Code:     "symbol-out-of-bounds",
				Message:  fmt.Sprintf("symbol %q value 0x%x is outside section %d (%s) [0x%x,0x%x)", s.Name, s.Value, sec.Index, sec.Name, sec.Addr, end),
			})
			continue
		}
		if s.Size > 0 && (s.Value > end || s.Size > end-s.Value) {
			s.OutOfBounds = true
			f.Diagnostics = append(f.Diagnostics, Diagnostic{
				Severity: SeverityWarning,
				Code:     "symbol-size-out-of-bounds",
				Message:  fmt.Sprintf("symbol %q [0x%x,0x%x) extends past section %d (%s) ending at 0x%x", s.Name, s.Value, s.Value+s.Size, sec.Index, sec.Name, end),
			})
		}
	}
}

func (f *File) readRelocs(opt Options) {
	seen := 0
	for i := range f.Sections {
		sec := &f.Sections[i]
		if sec.Type != elf.SHT_RELA && sec.Type != elf.SHT_REL {
			continue
		}
		if len(sec.Data) == 0 {
			f.Diagnostics = append(f.Diagnostics, Diagnostic{
				Severity: SeverityWarning,
				Code:     "empty-reloc-section",
				Message:  fmt.Sprintf("relocation section %d (%s) has no data", i, sec.Name),
			})
			continue
		}
		tableSyms, ok := f.SymbolTables[int(sec.Link)]
		if !ok {
			f.Diagnostics = append(f.Diagnostics, Diagnostic{
				Severity: SeverityWarning,
				Code:     "bad-reloc-symbol-table",
				Message:  fmt.Sprintf("relocation section %d (%s) links to unread symbol table %d", i, sec.Name, sec.Link),
			})
			continue
		}
		if int(sec.Info) >= len(f.Sections) || sec.Info == 0 {
			f.Diagnostics = append(f.Diagnostics, Diagnostic{
				Severity: SeverityWarning,
				Code:     "bad-reloc-target-section",
				Message:  fmt.Sprintf("relocation section %d (%s) targets invalid section %d", i, sec.Name, sec.Info),
			})
			continue
		}
		target := f.Sections[sec.Info]
		entSize := int(sec.EntSize)
		rela := sec.Type == elf.SHT_RELA
		if entSize == 0 {
			if rela {
				entSize = 24
			} else {
				entSize = 16
			}
		}
		need := 16
		if rela {
			need = 24
		}
		if entSize < need {
			f.Diagnostics = append(f.Diagnostics, Diagnostic{
				Severity: SeverityWarning,
				Code:     "bad-reloc-entry-size",
				Message:  fmt.Sprintf("relocation section %d (%s) has entry size %d; need at least %d", i, sec.Name, entSize, need),
			})
			continue
		}
		count := len(sec.Data) / entSize
		for j := 0; j < count; j++ {
			if seen >= opt.MaxRelocs {
				f.Diagnostics = append(f.Diagnostics, Diagnostic{
					Severity: SeverityWarning,
					Code:     "reloc-limit",
					Message:  fmt.Sprintf("relocation limit %d reached; remaining records ignored", opt.MaxRelocs),
				})
				return
			}
			seen++
			raw := sec.Data[j*entSize : j*entSize+need]
			rOffset := binary.LittleEndian.Uint64(raw[0:8])
			rInfo := binary.LittleEndian.Uint64(raw[8:16])
			rel := Reloc{
				Section:      int(sec.Info),
				Type:         uint32(rInfo),
				SymbolIndex:  int(rInfo >> 32),
				SourceOffset: uint64(j * entSize),
				SymbolShndx:  uint16(elf.SHN_UNDEF),
			}
			if rela {
				rel.Addend = int64(binary.LittleEndian.Uint64(raw[16:24]))
				rel.HasAddend = true
			}
			switch f.Type {
			case elf.ET_REL:
				rel.Address = target.Addr + rOffset
			default:
				rel.Address = rOffset
			}
			if target.Size > 0 && (rel.Address < target.Addr || rel.Address >= target.Addr+target.Size) {
				f.Diagnostics = append(f.Diagnostics, Diagnostic{
					Severity: SeverityWarning,
					Code:     "reloc-out-of-bounds",
					Message:  fmt.Sprintf("relocation %d in %s targets 0x%x outside section %d (%s)", j, sec.Name, rel.Address, target.Index, target.Name),
				})
				continue
			}
			if rel.SymbolIndex < 0 || rel.SymbolIndex >= len(tableSyms) {
				rel.Unresolved = "symbol index out of range"
				f.Diagnostics = append(f.Diagnostics, Diagnostic{
					Severity: SeverityWarning,
					Code:     "reloc-bad-symbol",
					Message:  fmt.Sprintf("relocation %d in %s has invalid symbol index %d (table has %d entries)", j, sec.Name, rel.SymbolIndex, len(tableSyms)),
				})
				f.Relocs = append(f.Relocs, rel)
				continue
			}
			sym := tableSyms[rel.SymbolIndex]
			rel.SymbolName = sym.Name
			rel.SymbolValue = sym.Value
			rel.SymbolShndx = sym.SectionIndex
			rel.SymbolType = sym.Type
			if !rel.HasAddend && sym.Defined && (rel.Type == rX8664PC32 || rel.Type == rX8664PLT32 || rel.Type == rX8664PC64) {
				// Implicit addend for SHT_REL lives in the relocated field.
				rel.Addend = readImplicitAddend(f, target, rel.Address, rel.Type)
			}
			f.Relocs = append(f.Relocs, rel)
		}
	}
}

const (
	rX8664PC32  = 2
	rX8664PLT32 = 4
	rX8664PC64  = 24
)

func readImplicitAddend(f *File, target Section, addr uint64, typ uint32) int64 {
	if len(target.Data) == 0 || addr < target.Addr {
		return 0
	}
	off := addr - target.Addr
	width := 4
	if typ == rX8664PC64 {
		width = 8
	}
	if off+uint64(width) > uint64(len(target.Data)) {
		return 0
	}
	if width == 8 {
		return int64(binary.LittleEndian.Uint64(target.Data[off : off+8]))
	}
	return int64(int32(binary.LittleEndian.Uint32(target.Data[off : off+4])))
}

// Section returns the section with the given index.
func (f *File) Section(index int) (Section, bool) {
	if index < 0 || index >= len(f.Sections) {
		return Section{}, false
	}
	return f.Sections[index], true
}

// ExecutableSections returns the indices of sections carrying code.
func (f *File) ExecutableSections() []int {
	var out []int
	for i := range f.Sections {
		if f.Sections[i].Executable && f.Sections[i].Size > 0 {
			out = append(out, i)
		}
	}
	return out
}

// describeELFBError keeps debug/elf's terse parse errors understandable.
func describeELFBError(err error) error {
	var fmtErr *elf.FormatError
	if errors.As(err, &fmtErr) {
		return fmt.Errorf("malformed ELF: %s", fmtErr.Error())
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return errors.New("truncated file")
	}
	return err
}
