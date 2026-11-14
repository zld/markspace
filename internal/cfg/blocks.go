// Package cfg turns a function's instruction stream into basic blocks.
//
// Blocks are delimited by control-transfer instructions (conditional and
// unconditional jumps, calls, and returns), by branch targets, and by the end
// of the function.  Bytes that Capstone cannot decode end the current block and
// are recorded with their offset; decoding resumes one byte later so a single
// bad byte does not hide the rest of the function.
package cfg

import (
	"context"
	"fmt"
	"sort"

	"bbdiff/internal/disasm"
)

// EdgeKind classifies a block exit.
type EdgeKind string

const (
	// EdgeFallthrough is the implicit next block (conditional branches and
	// the instruction after a call).
	EdgeFallthrough EdgeKind = "fallthrough"
	// EdgeBranch is a taken direct jump.
	EdgeBranch EdgeKind = "branch"
	// EdgeCall is a direct call.
	EdgeCall EdgeKind = "call"
	// EdgeIndirect is a computed jump or call.
	EdgeIndirect EdgeKind = "indirect"
)

// Edge is one control-flow exit from a block.
type Edge struct {
	Kind   EdgeKind `json:"kind"`
	Target uint64   `json:"target,omitempty"`
	// Symbol is the name of the function or object at Target when known, empty
	// otherwise.  It is filled by the caller that owns the symbol table.
	Symbol string `json:"symbol,omitempty"`
}

// Terminator describes why a block ended.
type Terminator string

const (
	TermFallthrough Terminator = "fallthrough"
	TermCondJump    Terminator = "conditional-jump"
	TermJump        Terminator = "jump"
	TermCall        Terminator = "call"
	TermReturn      Terminator = "return"
	TermInterrupt   Terminator = "interrupt"
	TermInvalid     Terminator = "invalid"
	TermFunctionEnd Terminator = "function-end"
)

// Instruction is a decoded instruction, or an invalid-byte marker.
type Instruction struct {
	disasm.Insn
	// Invalid marks a byte range Capstone could not decode.  Addr is the
	// first undecodable byte and Size is 1 (the decoder resumes after it).
	Invalid bool
}

// Block is a maximal straight-line instruction sequence.
type Block struct {
	Function     string
	Address      uint64
	End          uint64
	Instructions []Instruction
	Edges        []Edge
	Terminator   Terminator
	Invalid      bool
	// DecodeErrors collects offsets of undecodable bytes inside this block.
	DecodeErrors []uint64
}

// InstructionCount returns the number of decoded instructions (invalid markers
// are not counted).
func (b *Block) InstructionCount() int {
	n := 0
	for i := range b.Instructions {
		if !b.Instructions[i].Invalid {
			n++
		}
	}
	return n
}

// Options bounds decoding work.
type Options struct {
	// MaxInstructions caps decoded instructions per function.  A function that
	// exceeds it is truncated deterministically.
	MaxInstructions int
	// Context cancels long decodes.
	Context context.Context
}

// DefaultOptions returns conservative limits.
func DefaultOptions() Options {
	return Options{MaxInstructions: 200000, Context: context.Background()}
}

// Build disassembles code at base and splits it into basic blocks.  code must
// be the bytes of exactly one function.
func Build(engine *disasm.Engine, function string, base uint64, code []byte, opt Options) ([]*Block, []uint64, error) {
	if len(code) == 0 {
		return nil, nil, nil
	}
	if opt.MaxInstructions <= 0 {
		opt.MaxInstructions = DefaultOptions().MaxInstructions
	}
	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	opt.Context = ctx

	insns, badOffsets, err := decodeAll(engine, base, code, opt)
	if err != nil {
		return nil, nil, fmt.Errorf("cfg: decode %s at 0x%x: %w", function, base, err)
	}
	if len(insns) == 0 {
		return nil, badOffsets, nil
	}

	end := base + uint64(len(code))
	leaders := map[uint64]bool{base: true}
	for i := range insns {
		ins := &insns[i]
		if ins.Invalid {
			leaders[ins.Address] = true
			if ins.Address+1 < end {
				leaders[ins.Address+1] = true
			}
			continue
		}
		if ins.TerminatesBlock() {
			if next := ins.Address + uint64(ins.Size); next < end {
				leaders[next] = true
			}
		}
		if target, ok := ins.DirectTarget(); ok {
			if target >= base && target < end {
				leaders[target] = true
			}
		}
		// Capstone sometimes attributes branch targets to the first immediate
		// operand; that is the only case we treat as a leader above.
	}

	// A block that starts at a leader address must not include instructions
	// before it, and a leader that falls inside another instruction is
	// unreachable; the latter is left unmerged (it will simply not appear).
	var blocks []*Block
	var cur *Block
	flush := func(term Terminator) {
		if cur == nil || len(cur.Instructions) == 0 {
			cur = nil
			return
		}
		cur.Terminator = term
		blocks = append(blocks, cur)
		cur = nil
	}

	for i := range insns {
		ins := insns[i]
		if leaders[ins.Address] {
			if cur != nil {
				flush(termFor(cur))
			}
			cur = &Block{Function: function, Address: ins.Address}
		}
		if cur == nil {
			cur = &Block{Function: function, Address: ins.Address}
		}
		cur.Instructions = append(cur.Instructions, ins)
		cur.End = ins.Address + uint64(ins.Size)
		if ins.Invalid {
			cur.Invalid = true
			cur.DecodeErrors = append(cur.DecodeErrors, ins.Address)
			flush(TermInvalid)
			continue
		}
		if ins.TerminatesBlock() {
			flush(termFor(cur))
		}
	}
	flush(TermFunctionEnd)

	// Annotate edges and drop blocks whose start address is not the address of
	// one of their instructions (defensive; should not happen).
	filtered := blocks[:0]
	for _, b := range blocks {
		if len(b.Instructions) == 0 || b.Instructions[0].Address != b.Address {
			continue
		}
		b.Edges = edgesFor(b, base, end)
		filtered = append(filtered, b)
	}
	sort.SliceStable(filtered, func(i, j int) bool { return filtered[i].Address < filtered[j].Address })
	return filtered, badOffsets, nil
}

func termFor(b *Block) Terminator {
	if b == nil || len(b.Instructions) == 0 {
		return TermFunctionEnd
	}
	last := b.Instructions[len(b.Instructions)-1]
	if last.Invalid {
		return TermInvalid
	}
	switch {
	case last.Return:
		return TermReturn
	case last.Call:
		return TermCall
	case last.Jump && last.Conditional:
		return TermCondJump
	case last.Jump:
		return TermJump
	case last.Groups&(1<<disasm.GroupInterrupt) != 0:
		return TermInterrupt
	default:
		return TermFallthrough
	}
}

func edgesFor(b *Block, base, end uint64) []Edge {
	last := b.Instructions[len(b.Instructions)-1]
	next := last.Address + uint64(last.Size)
	var edges []Edge
	switch {
	case last.Invalid:
		return nil
	case last.Return:
		return nil
	case last.Call:
		if target, ok := last.DirectTarget(); ok {
			edges = append(edges, Edge{Kind: EdgeCall, Target: target})
		} else {
			edges = append(edges, Edge{Kind: EdgeIndirect})
		}
		if next < end {
			edges = append(edges, Edge{Kind: EdgeFallthrough, Target: next})
		}
	case last.Jump:
		if target, ok := last.DirectTarget(); ok {
			edges = append(edges, Edge{Kind: EdgeBranch, Target: target})
		} else {
			edges = append(edges, Edge{Kind: EdgeIndirect})
		}
		if last.Conditional && next < end {
			edges = append(edges, Edge{Kind: EdgeFallthrough, Target: next})
		}
	case last.Groups&(1<<disasm.GroupInterrupt) != 0:
		return nil
	default:
		if next < end {
			edges = append(edges, Edge{Kind: EdgeFallthrough, Target: next})
		}
	}
	// Only intra-function edges are of interest to the matcher; external
	// branch/call targets stay in the edge list for reporting.
	_ = base
	return edges
}

// decodeAll decodes code linearly, resynchronising one byte after every
// undecodable byte.
func decodeAll(engine *disasm.Engine, base uint64, code []byte, opt Options) ([]Instruction, []uint64, error) {
	var out []Instruction
	var bad []uint64
	pos := 0
	budget := opt.MaxInstructions
	for pos < len(code) {
		if budget <= 0 {
			break
		}
		if err := opt.Context.Err(); err != nil {
			return nil, nil, err
		}
		chunk := code[pos:]
		limit := budget
		if limit > 4096 {
			limit = 4096
		}
		res, err := engine.Disassemble(chunk, base+uint64(pos), limit)
		if err != nil {
			return nil, nil, err
		}
		for _, ins := range res.Insns {
			out = append(out, Instruction{Insn: ins})
		}
		budget -= len(res.Insns)
		advance := res.Consumed
		if res.BadOffset != 0 {
			off := int(res.BadOffset - base)
			if off < pos || off >= len(code) {
				// Defensive: the engine must not point outside the buffer.
				bad = append(bad, base+uint64(pos))
				pos++
				continue
			}
			out = append(out, Instruction{
				Insn:    disasm.Insn{Address: res.BadOffset, Size: 1},
				Invalid: true,
			})
			bad = append(bad, res.BadOffset)
			pos = off + 1
			continue
		}
		if advance <= 0 {
			// No progress; stop to guarantee termination.
			bad = append(bad, base+uint64(pos))
			break
		}
		pos += advance
	}
	// Instructions from repeated cs_disasm calls are already in ascending
	// address order, but sort to be explicit and stable.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	sort.Slice(bad, func(i, j int) bool { return bad[i] < bad[j] })
	return out, bad, nil
}
