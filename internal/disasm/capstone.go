// Package disasm is a small, defensive cgo binding around the Capstone 5
// disassembly engine.
//
// Only the pieces bbdiff needs are exposed: x86-64 decoding with instruction
// detail (operands and encoding offsets).  Every Capstone handle is owned by a
// single Engine; an Engine is not safe for concurrent use, callers are
// expected to create one Engine per worker goroutine.  Close releases the
// underlying cs_handle and must be called exactly once.
package disasm

/*
#cgo linux CFLAGS: -I/usr/include -I/usr/local/include
#cgo linux LDFLAGS: -lcapstone
#cgo darwin CFLAGS: -I/opt/homebrew/include -I/usr/local/include
#cgo darwin LDFLAGS: -L/opt/homebrew/lib -L/usr/local/lib -lcapstone -Wl,-rpath,/opt/homebrew/lib -Wl,-rpath,/usr/local/lib

#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <capstone/capstone.h>

static int bbdiff_open(csh *handle) {
	cs_err err = cs_open(CS_ARCH_X86, CS_MODE_64, handle);
	if (err != CS_ERR_OK) {
		return (int)err;
	}
	err = cs_option(*handle, CS_OPT_DETAIL, CS_OPT_ON);
	if (err != CS_ERR_OK) {
		cs_close(handle);
		return (int)err;
	}
	return 0;
}

static const char *bbdiff_error(int code) {
	return cs_strerror((cs_err)code);
}

static int bbdiff_group(csh handle, const cs_insn *insn, unsigned int group) {
	return cs_insn_group(handle, insn, group) ? 1 : 0;
}

static int bbdiff_op_count(const cs_insn *insn) {
	if (insn->detail == NULL) {
		return 0;
	}
	return (int)insn->detail->x86.op_count;
}

static int bbdiff_op_type(const cs_insn *insn, int i) {
	return (int)insn->detail->x86.operands[i].type;
}

static int bbdiff_op_reg(const cs_insn *insn, int i) {
	return (int)insn->detail->x86.operands[i].reg;
}

static int64_t bbdiff_op_imm(const cs_insn *insn, int i) {
	return insn->detail->x86.operands[i].imm;
}

static int bbdiff_op_size(const cs_insn *insn, int i) {
	return (int)insn->detail->x86.operands[i].size;
}

static int bbdiff_mem_segment(const cs_insn *insn, int i) {
	return (int)insn->detail->x86.operands[i].mem.segment;
}

static int bbdiff_mem_base(const cs_insn *insn, int i) {
	return (int)insn->detail->x86.operands[i].mem.base;
}

static int bbdiff_mem_index(const cs_insn *insn, int i) {
	return (int)insn->detail->x86.operands[i].mem.index;
}

static int32_t bbdiff_mem_scale(const cs_insn *insn, int i) {
	return (int32_t)insn->detail->x86.operands[i].mem.scale;
}

static int64_t bbdiff_mem_disp(const cs_insn *insn, int i) {
	return insn->detail->x86.operands[i].mem.disp;
}

static int bbdiff_disp_offset(const cs_insn *insn) {
	return (int)insn->detail->x86.encoding.disp_offset;
}

static int bbdiff_disp_size(const cs_insn *insn) {
	return (int)insn->detail->x86.encoding.disp_size;
}

static int bbdiff_imm_offset(const cs_insn *insn) {
	return (int)insn->detail->x86.encoding.imm_offset;
}

static int bbdiff_imm_size(const cs_insn *insn) {
	return (int)insn->detail->x86.encoding.imm_size;
}

static const char *bbdiff_reg_name(csh handle, unsigned int reg) {
	return cs_reg_name(handle, reg);
}

static void bbdiff_version(int *major, int *minor) {
	int combined = cs_version(major, minor);
	(void)combined;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"
)

// Group identifies the Capstone instruction groups that are relevant for
// basic-block construction.
type Group uint8

const (
	GroupInvalid Group = iota
	GroupJump
	GroupCall
	GroupReturn
	GroupInterrupt
	GroupIRet
	GroupPrivilege
	GroupBranchRelative
)

// OperandType mirrors x86_op_type.
type OperandType uint8

const (
	OperandInvalid OperandType = iota
	OperandRegister
	OperandImmediate
	OperandMemory
)

// Mem is an x86 memory operand.
type Mem struct {
	Segment     uint16
	Base        uint16
	Index       uint16
	SegmentName string
	BaseName    string
	IndexName   string
	Scale       int32
	Disp        int64
}

// Operand is a decoded instruction operand.
type Operand struct {
	Type    OperandType
	Reg     uint16
	RegName string
	Imm     int64
	Mem     Mem
	Size    uint8
}

// Insn is a decoded instruction.  Bytes contains a copy of the raw encoding;
// the caller may retain it after the decode result is released.
type Insn struct {
	Address     uint64
	Size        uint8
	ID          uint32
	Mnemonic    string
	OpStr       string
	Bytes       []byte
	Operands    []Operand
	Groups      uint8 // bitmask indexed by Group
	Call        bool
	Jump        bool
	Conditional bool
	Return      bool
	Indirect    bool

	// Encoding byte ranges for the displacement / immediate fields, relative
	// to Address.  A size of zero means the field is absent.  They are used
	// to blank address-dependent bytes during canonicalisation.
	DispOff  uint8
	DispSize uint8
	ImmOff   uint8
	ImmSize  uint8
}

// Result is returned by Disassemble.  Consumed is the number of input bytes
// that decoded successfully.  When BadOffset is non-zero the decoder stopped
// because the byte at BadOffset was not a valid x86-64 instruction.
type Result struct {
	Insns     []Insn
	Consumed  int
	BadOffset uint64
	Truncated bool // stopped because maxInsns was reached
}

// Version describes the Capstone library in use.
type Version struct {
	Major int
	Minor int
}

func libVersion() Version {
	var major, minor C.int
	C.bbdiff_version(&major, &minor)
	return Version{Major: int(major), Minor: int(minor)}
}

// SupportedVersion reports the Capstone version bbdiff is linked against.
func SupportedVersion() Version { return libVersion() }

// Engine owns a single Capstone handle.
type Engine struct {
	mu     sync.Mutex
	handle C.csh
	closed bool
}

// ErrClosed is returned when an Engine is used after Close.
var ErrClosed = errors.New("disasm: engine is closed")

// NewEngine opens a Capstone x86-64 engine with instruction detail enabled.
func NewEngine() (*Engine, error) {
	v := libVersion()
	if v.Major != 5 {
		return nil, fmt.Errorf("disasm: Capstone 5.x is required, linked library reports %d.%d", v.Major, v.Minor)
	}
	var handle C.csh
	if code := C.bbdiff_open(&handle); code != 0 {
		return nil, fmt.Errorf("disasm: cs_open failed: %s", C.GoString(C.bbdiff_error(C.int(code))))
	}
	return &Engine{handle: handle}, nil
}

// Close releases the Capstone handle.  It is idempotent.
func (e *Engine) Close() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	C.cs_close(&e.handle)
	e.handle = 0
	return nil
}

// Disassemble decodes code at the given virtual base address.  maxInsns bounds
// the amount of work and memory consumed; values <= 0 are rejected.
func (e *Engine) Disassemble(code []byte, base uint64, maxInsns int) (Result, error) {
	if maxInsns <= 0 {
		return Result{}, errors.New("disasm: maxInsns must be positive")
	}
	if len(code) == 0 {
		return Result{}, errors.New("disasm: empty code buffer")
	}
	if uint64(len(code)) > 1<<31 {
		return Result{}, fmt.Errorf("disasm: code buffer too large (%d bytes)", len(code))
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.handle == 0 {
		return Result{}, ErrClosed
	}

	var insn *C.cs_insn
	count := C.cs_disasm(
		e.handle,
		(*C.uint8_t)(unsafe.Pointer(&code[0])),
		C.size_t(len(code)),
		C.uint64_t(base),
		C.size_t(maxInsns),
		&insn,
	)
	if count == 0 {
		if errno := C.cs_errno(e.handle); errno != C.CS_ERR_OK {
			return Result{}, fmt.Errorf("disasm: cs_disasm failed at 0x%x: %s", base, C.GoString(C.bbdiff_error(C.int(errno))))
		}
		return Result{Insns: nil, Consumed: 0, BadOffset: base}, nil
	}
	defer C.cs_free(insn, count)

	n := int(count)
	raw := unsafe.Slice(insn, n)
	insns := make([]Insn, 0, n)
	var consumed int
	for i := range raw {
		ci := &raw[i]
		decoded := e.convert(ci)
		insns = append(insns, decoded)
		consumed = int(uint64(ci.address)+uint64(ci.size)) - int(base)
	}
	if consumed < 0 || consumed > len(code) {
		return Result{}, fmt.Errorf("disasm: decoder reported an out-of-range offset at 0x%x", base)
	}
	res := Result{Insns: insns, Consumed: consumed}
	if n == maxInsns && consumed < len(code) {
		res.Truncated = true
	} else if consumed < len(code) {
		res.BadOffset = base + uint64(consumed)
	}
	return res, nil
}

func (e *Engine) convert(ci *C.cs_insn) Insn {
	size := uint8(ci.size)
	if size > 24 {
		size = 24
	}
	insn := Insn{
		Address:  uint64(ci.address),
		Size:     size,
		ID:       uint32(ci.id),
		Mnemonic: C.GoString((*C.char)(unsafe.Pointer(&ci.mnemonic[0]))),
		OpStr:    C.GoString((*C.char)(unsafe.Pointer(&ci.op_str[0]))),
		Bytes:    C.GoBytes(unsafe.Pointer(&ci.bytes[0]), C.int(size)),
		DispOff:  uint8(C.bbdiff_disp_offset(ci)),
		DispSize: uint8(C.bbdiff_disp_size(ci)),
		ImmOff:   uint8(C.bbdiff_imm_offset(ci)),
		ImmSize:  uint8(C.bbdiff_imm_size(ci)),
	}

	if C.bbdiff_group(e.handle, ci, C.CS_GRP_JUMP) != 0 {
		insn.Groups |= 1 << GroupJump
		insn.Jump = true
	}
	if C.bbdiff_group(e.handle, ci, C.CS_GRP_CALL) != 0 {
		insn.Groups |= 1 << GroupCall
		insn.Call = true
	}
	if C.bbdiff_group(e.handle, ci, C.CS_GRP_RET) != 0 {
		insn.Groups |= 1 << GroupReturn
		insn.Return = true
	}
	if C.bbdiff_group(e.handle, ci, C.CS_GRP_IRET) != 0 {
		insn.Groups |= 1 << GroupIRet
		insn.Return = true
	}
	if C.bbdiff_group(e.handle, ci, C.CS_GRP_INT) != 0 {
		insn.Groups |= 1 << GroupInterrupt
	}
	if C.bbdiff_group(e.handle, ci, C.CS_GRP_PRIVILEGE) != 0 {
		insn.Groups |= 1 << GroupPrivilege
	}
	if C.bbdiff_group(e.handle, ci, C.CS_GRP_BRANCH_RELATIVE) != 0 {
		insn.Groups |= 1 << GroupBranchRelative
	}

	// Capstone classifies loop/jcxz as jumps; only the plain "jmp" family is
	// unconditional.
	if insn.Jump && insn.Mnemonic != "jmp" && insn.Mnemonic != "ljmp" {
		insn.Conditional = true
	}

	opCount := int(C.bbdiff_op_count(ci))
	if opCount > 8 {
		opCount = 8
	}
	insn.Operands = make([]Operand, 0, opCount)
	for i := 0; i < opCount; i++ {
		op := Operand{
			Type: OperandType(C.bbdiff_op_type(ci, C.int(i))),
			Size: uint8(C.bbdiff_op_size(ci, C.int(i))),
		}
		switch op.Type {
		case OperandRegister:
			op.Reg = uint16(C.bbdiff_op_reg(ci, C.int(i)))
			op.RegName = regName(e.handle, C.uint(op.Reg))
		case OperandImmediate:
			op.Imm = int64(C.bbdiff_op_imm(ci, C.int(i)))
		case OperandMemory:
			op.Mem = Mem{
				Segment: uint16(C.bbdiff_mem_segment(ci, C.int(i))),
				Base:    uint16(C.bbdiff_mem_base(ci, C.int(i))),
				Index:   uint16(C.bbdiff_mem_index(ci, C.int(i))),
				Scale:   int32(C.bbdiff_mem_scale(ci, C.int(i))),
				Disp:    int64(C.bbdiff_mem_disp(ci, C.int(i))),
			}
			op.Mem.SegmentName = regName(e.handle, C.uint(op.Mem.Segment))
			op.Mem.BaseName = regName(e.handle, C.uint(op.Mem.Base))
			op.Mem.IndexName = regName(e.handle, C.uint(op.Mem.Index))
		}
		insn.Operands = append(insn.Operands, op)
	}

	if insn.Call || insn.Jump {
		if len(insn.Operands) == 0 || insn.Operands[0].Type != OperandImmediate {
			insn.Indirect = true
		}
	}
	return insn
}

func regName(handle C.csh, reg C.uint) string {
	if reg == 0 {
		return ""
	}
	name := C.bbdiff_reg_name(handle, reg)
	if name == nil {
		return ""
	}
	return C.GoString(name)
}

// DirectTarget returns the immediate branch target, if the instruction has a
// direct control-flow target.
func (i Insn) DirectTarget() (uint64, bool) {
	if !i.Call && !i.Jump {
		return 0, false
	}
	if len(i.Operands) == 0 || i.Operands[0].Type != OperandImmediate {
		return 0, false
	}
	return uint64(i.Operands[0].Imm), true
}

// TerminatesBlock reports whether the instruction must end a basic block.
func (i Insn) TerminatesBlock() bool {
	return i.Call || i.Jump || i.Return || i.Groups&(1<<GroupInterrupt) != 0
}
