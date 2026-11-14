package disasm_test

import (
	"errors"
	"testing"

	"bbdiff/internal/disasm"
)

func newEngine(t *testing.T) *disasm.Engine {
	t.Helper()
	e, err := disasm.NewEngine()
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	t.Cleanup(func() {
		if err := e.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return e
}

func TestVersion(t *testing.T) {
	v := disasm.SupportedVersion()
	if v.Major != 5 {
		t.Fatalf("Capstone major version = %d, want 5", v.Major)
	}
}

func TestDisassembleBasicFunction(t *testing.T) {
	e := newEngine(t)
	code := []byte{
		0x55,             // push rbp
		0x48, 0x89, 0xe5, // mov rbp, rsp
		0x83, 0xff, 0x02, // cmp edi, 2
		0x74, 0x02, // je +2
		0x31, 0xc0, // xor eax, eax
		0xc3, // ret
	}
	res, err := e.Disassemble(code, 0x401000, 100)
	if err != nil {
		t.Fatalf("Disassemble: %v", err)
	}
	if len(res.Insns) != 6 {
		t.Fatalf("decoded %d instructions, want 6", len(res.Insns))
	}
	if res.Consumed != len(code) {
		t.Errorf("consumed %d bytes, want %d", res.Consumed, len(code))
	}
	if res.BadOffset != 0 {
		t.Errorf("unexpected bad offset %#x", res.BadOffset)
	}
	// Control-flow classification.
	if !res.Insns[3].Jump || !res.Insns[3].Conditional {
		t.Errorf("je not classified as conditional jump: %+v", res.Insns[2])
	}
	if !res.Insns[5].Return {
		t.Errorf("ret not classified as return")
	}
	if target, ok := res.Insns[3].DirectTarget(); !ok || target != 0x40100b {
		t.Errorf("je target = %#x, %v; want 0x40100a", target, ok)
	}
	// Encoding offsets.
	if res.Insns[2].ImmOff != 2 || res.Insns[2].ImmSize != 1 {
		t.Errorf("cmp imm offsets = %d/%d, want 2/1", res.Insns[2].ImmOff, res.Insns[2].ImmSize)
	}
	if res.Insns[3].ImmOff != 1 || res.Insns[3].ImmSize != 1 {
		t.Errorf("je imm offsets = %d/%d, want 1/1", res.Insns[3].ImmOff, res.Insns[3].ImmSize)
	}
}

func TestDisassembleRejectsEmptyBuffer(t *testing.T) {
	e := newEngine(t)
	if _, err := e.Disassemble(nil, 0, 10); err == nil {
		t.Fatal("Disassemble accepted an empty buffer")
	}
}

func TestDisassembleRejectsBadLimit(t *testing.T) {
	e := newEngine(t)
	if _, err := e.Disassemble([]byte{0x90}, 0, 0); err == nil {
		t.Fatal("Disassemble accepted maxInsns=0")
	}
}

func TestDisassembleStopsAtInvalidByte(t *testing.T) {
	e := newEngine(t)
	// 0xf0 0x0f is an invalid prefix sequence for x86-64.
	code := []byte{0x90, 0xf0, 0x0f, 0x90}
	res, err := e.Disassemble(code, 0x1000, 100)
	if err != nil {
		t.Fatalf("Disassemble: %v", err)
	}
	if len(res.Insns) == 0 {
		t.Fatal("no instructions decoded")
	}
	if res.BadOffset != 0x1001 {
		t.Fatalf("BadOffset = %#x, want 0x1001", res.BadOffset)
	}
}

func TestDisassembleTruncatesAtLimit(t *testing.T) {
	e := newEngine(t)
	nops := make([]byte, 64)
	for i := range nops {
		nops[i] = 0x90
	}
	res, err := e.Disassemble(nops, 0, 10)
	if err != nil {
		t.Fatalf("Disassemble: %v", err)
	}
	if len(res.Insns) != 10 {
		t.Fatalf("decoded %d instructions, want 10", len(res.Insns))
	}
	if !res.Truncated {
		t.Fatal("result not marked truncated")
	}
}

func TestEngineCloseIsIdempotent(t *testing.T) {
	e, err := disasm.NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := e.Disassemble([]byte{0x90}, 0, 1); !errors.Is(err, disasm.ErrClosed) {
		t.Fatalf("Disassemble after Close: %v, want ErrClosed", err)
	}
}

func TestOperandsAndMemory(t *testing.T) {
	e := newEngine(t)
	// lea rdi, [rip + 0x10]
	code := []byte{0x48, 0x8d, 0x3d, 0x10, 0, 0, 0}
	res, err := e.Disassemble(code, 0x401000, 10)
	if err != nil {
		t.Fatal(err)
	}
	ins := res.Insns[0]
	if len(ins.Operands) != 2 {
		t.Fatalf("got %d operands, want 2", len(ins.Operands))
	}
	if ins.Operands[0].Type != disasm.OperandRegister || ins.Operands[0].RegName != "rdi" {
		t.Errorf("operand 0 = %+v, want rdi register", ins.Operands[0])
	}
	mem := ins.Operands[1]
	if mem.Type != disasm.OperandMemory {
		t.Fatalf("operand 1 type = %v, want memory", mem.Type)
	}
	if mem.Mem.BaseName != "rip" || mem.Mem.Disp != 0x10 {
		t.Errorf("memory operand = %+v, want rip+0x10", mem.Mem)
	}
	if ins.DispOff != 3 || ins.DispSize != 4 {
		t.Errorf("disp offsets = %d/%d, want 3/4", ins.DispOff, ins.DispSize)
	}
}
