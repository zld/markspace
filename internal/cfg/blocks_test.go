package cfg_test

import (
	"context"
	"testing"

	"bbdiff/internal/cfg"
	"bbdiff/internal/disasm"
)

func engine(t *testing.T) *disasm.Engine {
	t.Helper()
	e, err := disasm.NewEngine()
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func build(t *testing.T, code []byte, base uint64) ([]*cfg.Block, []uint64) {
	t.Helper()
	blocks, bad, err := cfg.Build(engine(t), "fn", base, code, cfg.DefaultOptions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return blocks, bad
}

func TestBlocksSplitOnConditionalBranch(t *testing.T) {
	// push rbp; je +2; xor eax,eax; ret
	code := []byte{
		0x55,
		0x74, 0x02,
		0x31, 0xc0,
		0xc3,
	}
	blocks, bad := build(t, code, 0x1000)
	if len(bad) != 0 {
		t.Fatalf("unexpected bad offsets: %v", bad)
	}
	// The taken edge target (0x1005) is beyond the buffer; the fallthrough
	// block starts at 0x1003.
	if len(blocks) < 2 {
		t.Fatalf("got %d blocks, want >= 2", len(blocks))
	}
	if blocks[0].Address != 0x1000 {
		t.Errorf("first block address = %#x", blocks[0].Address)
	}
	if blocks[0].Terminator != cfg.TermCondJump {
		t.Errorf("first block terminator = %v, want conditional-jump", blocks[0].Terminator)
	}
	if blocks[1].Address != 0x1003 {
		t.Errorf("second block address = %#x, want 0x1003", blocks[1].Address)
	}
}

func TestBlocksSplitOnCall(t *testing.T) {
	// call rel32 to +0x20 (outside), ret, ... nops
	code := []byte{
		0xe8, 0x1b, 0x00, 0x00, 0x00,
		0xc3,
		0x90, 0x90,
	}
	blocks, _ := build(t, code, 0x1000)
	// call ends a block; ret ends the next; the trailing nops form a third.
	if len(blocks) != 3 {
		t.Fatalf("got %d blocks, want 3 (call | ret | nops)", len(blocks))
	}
	if blocks[0].Terminator != cfg.TermCall {
		t.Errorf("terminator = %v, want call", blocks[0].Terminator)
	}
	// The call edge is recorded even though its target is external.
	var callEdge bool
	for _, e := range blocks[0].Edges {
		if e.Kind == cfg.EdgeCall && e.Target == 0x1005+0x1b {
			callEdge = true
		}
	}
	if !callEdge {
		t.Errorf("missing call edge: %+v", blocks[0].Edges)
	}
}

func TestBlocksSplitOnReturn(t *testing.T) {
	code := []byte{0x90, 0xc3, 0x90}
	blocks, _ := build(t, code, 0x1000)
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2", len(blocks))
	}
	if blocks[0].Terminator != cfg.TermReturn {
		t.Errorf("terminator = %v, want return", blocks[0].Terminator)
	}
	if len(blocks[0].Edges) != 0 {
		t.Errorf("return block has edges: %+v", blocks[0].Edges)
	}
}

func TestBranchTargetStartsBlock(t *testing.T) {
	// je over one instruction to a label; both target and fallthrough are
	// leaders.
	code := []byte{
		0x74, 0x01, // je +1 (targets 0x2003)
		0x90,       // fallthrough nop at 0x2002
		0x31, 0xc0, // target at 0x2003: xor eax, eax
		0xc3,
	}
	blocks, _ := build(t, code, 0x2000)
	if len(blocks) != 3 {
		t.Fatalf("got %d blocks, want 3", len(blocks))
	}
	addrs := []uint64{blocks[0].Address, blocks[1].Address, blocks[2].Address}
	want := []uint64{0x2000, 0x2002, 0x2003}
	for i := range want {
		if addrs[i] != want[i] {
			t.Fatalf("block addresses = %#x, want %#x", addrs, want)
		}
	}
}

func TestInvalidInstructionTerminatesBlock(t *testing.T) {
	// mov eax, 1; <invalid bytes>; mov eax, 2; ret
	code := []byte{
		0xb8, 0x01, 0x00, 0x00, 0x00,
		0xf0, 0x0f, // invalid prefix pair
		0xb8, 0x02, 0x00, 0x00, 0x00,
		0xc3,
	}
	blocks, bad := build(t, code, 0x3000)
	if len(bad) == 0 {
		t.Fatal("invalid bytes were not reported")
	}
	var sawInvalid bool
	for _, b := range blocks {
		if b.Invalid {
			sawInvalid = true
			if b.Terminator != cfg.TermInvalid {
				t.Errorf("invalid block terminator = %v", b.Terminator)
			}
			if len(b.DecodeErrors) == 0 {
				t.Error("invalid block has no decode error offset")
			}
		}
	}
	if !sawInvalid {
		t.Fatal("no block was marked invalid")
	}
	// Decoding must resume after the bad bytes, so the later instructions
	// still appear.
	var total int
	for _, b := range blocks {
		for _, ins := range b.Instructions {
			if !ins.Invalid && ins.Mnemonic == "mov" {
				total++
			}
		}
	}
	if total != 2 {
		t.Fatalf("decoded %d mov instructions after invalid bytes, want 2", total)
	}
}

func TestAllInvalidBytesTerminates(t *testing.T) {
	code := make([]byte, 8)
	for i := range code {
		code[i] = 0xf0
	}
	blocks, bad := build(t, code, 0x4000)
	if len(bad) == 0 {
		t.Fatal("invalid bytes not reported")
	}
	for _, b := range blocks {
		if b.InstructionCount() != 0 && !b.Invalid {
			t.Errorf("unexpected valid block: %+v", b)
		}
	}
}

func TestCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := cfg.Build(engine(t), "fn", 0, []byte{0x90}, cfg.Options{
		MaxInstructions: 10,
		Context:         ctx,
	})
	if err == nil {
		t.Fatal("Build ignored a cancelled context")
	}
}

func TestInstructionBudget(t *testing.T) {
	nops := make([]byte, 32)
	for i := range nops {
		nops[i] = 0x90
	}
	blocks, _, err := cfg.Build(engine(t), "fn", 0, nops, cfg.Options{MaxInstructions: 5})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	for _, b := range blocks {
		count += b.InstructionCount()
	}
	if count > 5 {
		t.Fatalf("decoded %d instructions, budget was 5", count)
	}
}

func TestEmptyCode(t *testing.T) {
	blocks, bad, err := cfg.Build(engine(t), "fn", 0, nil, cfg.DefaultOptions())
	if err != nil {
		t.Fatalf("Build(empty): %v", err)
	}
	if len(blocks) != 0 || len(bad) != 0 {
		t.Fatalf("empty code produced %d blocks, %d bad offsets", len(blocks), len(bad))
	}
}

func TestDeterministicBlockSplit(t *testing.T) {
	code := []byte{
		0x55, 0x48, 0x89, 0xe5,
		0x85, 0xf6, 0x74, 0x05,
		0xb8, 0x2a, 0, 0, 0,
		0xeb, 0x03,
		0xb8, 0x01, 0, 0, 0, 0xc3,
	}
	a, _ := build(t, code, 0x5000)
	b, _ := build(t, code, 0x5000)
	if len(a) != len(b) {
		t.Fatal("block counts differ between runs")
	}
	for i := range a {
		if a[i].Address != b[i].Address || a[i].Terminator != b[i].Terminator {
			t.Fatalf("block %d differs: %+v vs %+v", i, a[i], b[i])
		}
	}
}
