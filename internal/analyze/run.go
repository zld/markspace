// Package analyze wires the analysis pipeline together: ELF scanning,
// relocation normalisation, symbol alignment, disassembly, block construction,
// matching, and assembly of the report data.
//
// Functions are analysed concurrently with a bounded worker pool.  Each worker
// owns its own Capstone engine and closes it when it stops, including on
// cancellation and after a failed job.  A failure in one function is recorded
// and never discards the results already produced for other functions.
package analyze

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"bbdiff/internal/align"
	"bbdiff/internal/cfg"
	"bbdiff/internal/compare"
	"bbdiff/internal/disasm"
	"bbdiff/internal/elfscan"
	"bbdiff/internal/reloc"
)

// Options controls an analysis run.
type Options struct {
	// Workers bounds concurrent Capstone engines.  Zero selects a value based
	// on GOMAXPROCS, capped at 8.
	Workers int
	// MaxInstructionsPerFunction bounds decode work for one function.
	MaxInstructionsPerFunction int
	// MaxBlocksPerFunction bounds retained blocks for one function.  A longer
	// function is truncated deterministically and a diagnostic is emitted.
	MaxBlocksPerFunction int
	// ELF bounds the ELF reader.
	ELF elfscan.Options
	// Context cancels the run.  A cancelled run still returns the results
	// collected so far.
	Context context.Context
}

// DefaultOptions returns local-analysis defaults.
func DefaultOptions() Options {
	workers := runtime.GOMAXPROCS(0)
	if workers > 8 {
		workers = 8
	}
	if workers < 1 {
		workers = 1
	}
	return Options{
		Workers:                    workers,
		MaxInstructionsPerFunction: 200000,
		MaxBlocksPerFunction:       20000,
		ELF:                        elfscan.DefaultOptions(),
		Context:                    context.Background(),
	}
}

// Summary is the quantitative result of a run.
type Summary struct {
	Added     int `json:"added"`
	Removed   int `json:"removed"`
	Changed   int `json:"changed"`
	Uncertain int `json:"uncertain"`
}

// Diff describes one differing basic block.
type Diff struct {
	// Address is the block start on the primary side: the new build for
	// added, changed and uncertain blocks, the old build for removed blocks.
	Address string `json:"address"`
	// InstructionCount is the decoded instruction count on the primary side.
	InstructionCount int `json:"instruction_count"`
	// SHA256 is the canonical instruction hash on the primary side.
	SHA256 string `json:"sha256"`
	// Function is the owning function name.
	Function string `json:"function"`
	// Kind is added, removed, changed or uncertain.
	Kind string `json:"kind"`

	// OldAddress and NewAddress expose both sides when the block is matched.
	OldAddress string `json:"old_address,omitempty"`
	NewAddress string `json:"new_address,omitempty"`
	// OldSHA256 is the canonical hash on the old side for matched blocks.
	OldSHA256 string `json:"old_sha256,omitempty"`
	// ChangedInstructions counts instruction positions that differ.
	ChangedInstructions int `json:"changed_instructions,omitempty"`
	// Confidence is 0..100 for uncertain blocks.
	Confidence int `json:"confidence,omitempty"`
	// Reasons explains the classification.
	Reasons []string `json:"reasons,omitempty"`
}

// FunctionReport summarizes one function.
type FunctionReport struct {
	Name       string `json:"name"`
	OldAddress string `json:"old_address,omitempty"`
	NewAddress string `json:"new_address,omitempty"`
	OldBlocks  int    `json:"old_blocks"`
	NewBlocks  int    `json:"new_blocks"`
	Changed    int    `json:"changed"`
	Uncertain  int    `json:"uncertain"`
	Matched    int    `json:"matched"`
	// Status is "old-only", "new-only", "aligned" or "uncertain".
	Status string `json:"status"`
}

// Result is the complete analysis outcome.
type Result struct {
	Summary     Summary              `json:"summary"`
	Blocks      []Diff               `json:"blocks"`
	Functions   []FunctionReport     `json:"functions"`
	Diagnostics []elfscan.Diagnostic `json:"diagnostics"`
	OldPath     string               `json:"old_path"`
	NewPath     string               `json:"new_path"`
	// Cancelled reports that the context ended the run early.
	Cancelled bool `json:"cancelled,omitempty"`
	// Failures lists functions that could not be analysed.
	Failures []string `json:"failures,omitempty"`
}

type jobKind int

const (
	jobPair jobKind = iota
	jobOldOnly
	jobNewOnly
)

type job struct {
	kind      jobKind
	old       *align.Function
	new       *align.Function
	uncertain bool
}

// Run analyses oldPath against newPath.  A structural problem with either
// file returns an error; per-function problems are reported in the result.
func Run(oldPath, newPath string, opt Options) (*Result, error) {
	if opt.Workers <= 0 {
		opt.Workers = DefaultOptions().Workers
	}
	if opt.MaxInstructionsPerFunction <= 0 {
		opt.MaxInstructionsPerFunction = DefaultOptions().MaxInstructionsPerFunction
	}
	if opt.MaxBlocksPerFunction <= 0 {
		opt.MaxBlocksPerFunction = DefaultOptions().MaxBlocksPerFunction
	}
	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}

	oldFile, err := elfscan.Read(oldPath, opt.ELF)
	if err != nil {
		return nil, fmt.Errorf("old image: %w", err)
	}
	newFile, err := elfscan.Read(newPath, opt.ELF)
	if err != nil {
		return nil, fmt.Errorf("new image: %w", err)
	}

	res := &Result{OldPath: oldPath, NewPath: newPath}
	res.Diagnostics = append(res.Diagnostics, tagFile("old", oldFile.Diagnostics)...)
	res.Diagnostics = append(res.Diagnostics, tagFile("new", newFile.Diagnostics)...)

	oldRelocs := reloc.Normalize(oldFile)
	newRelocs := reloc.Normalize(newFile)
	res.Diagnostics = append(res.Diagnostics, tagFile("old", oldRelocs.Diagnostics)...)
	res.Diagnostics = append(res.Diagnostics, tagFile("new", newRelocs.Diagnostics)...)

	oldSyms := compare.NewSymbolIndex(oldFile)
	newSyms := compare.NewSymbolIndex(newFile)

	alignment := align.Align(oldFile, newFile)
	res.Diagnostics = append(res.Diagnostics, alignment.Diagnostics...)

	var jobs []job
	for i := range alignment.Pairs {
		jobs = append(jobs, job{kind: jobPair, old: alignment.Pairs[i].Old, new: alignment.Pairs[i].New})
	}
	for i := range alignment.Uncertain {
		jobs = append(jobs, job{kind: jobPair, old: alignment.Uncertain[i].Old, new: alignment.Uncertain[i].New, uncertain: true})
	}
	for _, fn := range alignment.Removed {
		jobs = append(jobs, job{kind: jobOldOnly, old: fn})
	}
	for _, fn := range alignment.Added {
		jobs = append(jobs, job{kind: jobNewOnly, new: fn})
	}

	env := &environment{
		oldFile: oldFile, newFile: newFile,
		oldRelocs: oldRelocs, newRelocs: newRelocs,
		oldSyms: oldSyms, newSyms: newSyms,
		opt: opt,
	}

	results := make([]jobResult, len(jobs))
	var next atomic.Int64
	next.Store(-1)
	var cancelled atomic.Bool

	workers := opt.Workers
	if workers > len(jobs) {
		workers = len(jobs)
	}
	if workers < 1 {
		workers = 1
	}

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			engine, err := disasm.NewEngine()
			if err != nil {
				for {
					idx := int(next.Add(1))
					if idx >= len(jobs) {
						return
					}
					results[idx] = jobResult{
						job:  jobs[idx],
						done: true,
						err:  fmt.Sprintf("disassembler unavailable: %v", err),
					}
				}
			}
			defer engine.Close()
			for {
				if ctx.Err() != nil {
					cancelled.Store(true)
					return
				}
				idx := int(next.Add(1))
				if idx >= len(jobs) {
					return
				}
				fr := env.process(ctx, engine, jobs[idx])
				fr.job = jobs[idx]
				fr.done = true
				results[idx] = fr
				if fr.cancelled {
					cancelled.Store(true)
					return
				}
			}
		}()
	}
	wg.Wait()

	res.Cancelled = cancelled.Load()
	skipped := 0
	for i := range results {
		fr := &results[i]
		if !fr.done {
			skipped++
			continue
		}
		if fr.err != "" {
			res.Failures = append(res.Failures, fr.err)
		}
		res.Diagnostics = append(res.Diagnostics, fr.diagnostics...)
		res.Functions = append(res.Functions, fr.reports...)
		for _, b := range fr.blocks {
			switch b.Kind {
			case string(compare.KindAdded):
				res.Summary.Added++
			case string(compare.KindRemoved):
				res.Summary.Removed++
			case string(compare.KindChanged):
				res.Summary.Changed++
			case string(compare.KindUncertain):
				res.Summary.Uncertain++
			}
			res.Blocks = append(res.Blocks, b)
		}
	}

	sort.SliceStable(res.Blocks, func(i, j int) bool {
		ai, aj := parseAddr(res.Blocks[i].Address), parseAddr(res.Blocks[j].Address)
		if ai != aj {
			return ai < aj
		}
		if res.Blocks[i].Function != res.Blocks[j].Function {
			return res.Blocks[i].Function < res.Blocks[j].Function
		}
		return res.Blocks[i].Kind < res.Blocks[j].Kind
	})
	sort.SliceStable(res.Functions, func(i, j int) bool {
		if res.Functions[i].Name != res.Functions[j].Name {
			return res.Functions[i].Name < res.Functions[j].Name
		}
		return res.Functions[i].NewAddress < res.Functions[j].NewAddress
	})
	sort.SliceStable(res.Diagnostics, func(i, j int) bool {
		a, b := res.Diagnostics[i], res.Diagnostics[j]
		ra, rb := severityRank(a.Severity), severityRank(b.Severity)
		if ra != rb {
			return ra < rb
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		return a.Message < b.Message
	})
	sort.Strings(res.Failures)
	if skipped > 0 {
		res.Diagnostics = append(res.Diagnostics, elfscan.Diagnostic{
			Severity: elfscan.SeverityWarning,
			Code:     "analysis-incomplete",
			Message:  fmt.Sprintf("%d of %d functions were not analysed because the run was cancelled", skipped, len(results)),
		})
	}

	if res.Blocks == nil {
		res.Blocks = []Diff{}
	}
	if res.Functions == nil {
		res.Functions = []FunctionReport{}
	}
	if res.Diagnostics == nil {
		res.Diagnostics = []elfscan.Diagnostic{}
	}
	return res, nil
}

type environment struct {
	oldFile, newFile     *elfscan.File
	oldRelocs, newRelocs reloc.Result
	oldSyms, newSyms     *compare.SymbolIndex
	opt                  Options
}

type jobResult struct {
	job         job
	done        bool
	err         string
	cancelled   bool
	blocks      []Diff
	reports     []FunctionReport
	diagnostics []elfscan.Diagnostic
}

// blocksOf builds the canonical blocks of one function.
func (env *environment) blocksOf(ctx context.Context, engine *disasm.Engine, fn *align.Function, old bool) ([]*compare.CanonBlock, []elfscan.Diagnostic, error) {
	f := env.newFile
	relocs := env.newRelocs
	syms := env.newSyms
	side := "new"
	if old {
		f = env.oldFile
		relocs = env.oldRelocs
		syms = env.oldSyms
		side = "old"
	}
	sec, ok := f.Section(fn.SectionIdx)
	if !ok || !sec.Executable {
		return nil, nil, fmt.Errorf("%s image: function %q is not in an executable section", side, fn.Name)
	}
	if fn.Address < sec.Addr || fn.Size == 0 || fn.Address-sec.Addr+fn.Size > uint64(len(sec.Data)) {
		return nil, nil, fmt.Errorf("%s image: function %q [0x%x,+%d) does not fit section %s", side, fn.Name, fn.Address, fn.Size, sec.Name)
	}
	code := sec.Data[fn.Address-sec.Addr : fn.Address-sec.Addr+fn.Size]
	blocks, _, err := cfg.Build(engine, fn.Name, fn.Address, code, cfg.Options{
		MaxInstructions: env.opt.MaxInstructionsPerFunction,
		Context:         ctx,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("%s image: function %q: %w", side, fn.Name, err)
	}
	var diags []elfscan.Diagnostic
	if len(blocks) > env.opt.MaxBlocksPerFunction {
		diags = append(diags, elfscan.Diagnostic{
			Severity: elfscan.SeverityWarning,
			Code:     "block-limit",
			Message:  fmt.Sprintf("%s image: function %q has %d blocks; truncated to %d", side, fn.Name, len(blocks), env.opt.MaxBlocksPerFunction),
		})
		blocks = blocks[:env.opt.MaxBlocksPerFunction]
	}
	canon := compare.CanonicalizeFunction(blocks, relocs, syms)
	for i := range blocks {
		for _, off := range blocks[i].DecodeErrors {
			diags = append(diags, elfscan.Diagnostic{
				Severity: elfscan.SeverityWarning,
				Code:     "invalid-instruction",
				Message:  fmt.Sprintf("%s image: function %q: undecodable byte at 0x%x", side, fn.Name, off),
			})
		}
		_ = i
	}
	return canon, diags, nil
}

func (env *environment) process(ctx context.Context, engine *disasm.Engine, j job) jobResult {
	fr := jobResult{}
	if err := ctx.Err(); err != nil {
		fr.cancelled = true
		fr.err = fmt.Sprintf("analysis cancelled: %v", err)
		return fr
	}

	var oldBlocks, newBlocks []*compare.CanonBlock
	oldErr, newErr := "", ""
	if j.old != nil {
		blocks, diags, err := env.blocksOf(ctx, engine, j.old, true)
		fr.diagnostics = append(fr.diagnostics, diags...)
		if err != nil {
			oldErr = err.Error()
		} else {
			oldBlocks = blocks
		}
	}
	if j.new != nil {
		blocks, diags, err := env.blocksOf(ctx, engine, j.new, false)
		fr.diagnostics = append(fr.diagnostics, diags...)
		if err != nil {
			newErr = err.Error()
		} else {
			newBlocks = blocks
		}
	}
	if ctx.Err() != nil {
		fr.cancelled = true
		fr.err = fmt.Sprintf("analysis cancelled: %v", ctx.Err())
		return fr
	}

	name := ""
	switch {
	case j.new != nil && j.old != nil:
		name = j.new.Name
	case j.new != nil:
		name = j.new.Name
	case j.old != nil:
		name = j.old.Name
	}

	if oldErr != "" || newErr != "" {
		// One side failed: still emit the other side's blocks so nothing is
		// lost, and report why.
		if oldErr != "" {
			fr.err = oldErr
		}
		if newErr != "" {
			if fr.err != "" {
				fr.err += "; "
			}
			fr.err += newErr
		}
		side := ""
		var blocks []*compare.CanonBlock
		switch {
		case oldErr != "" && newErr == "":
			side = "new"
			blocks = newBlocks
		case newErr != "" && oldErr == "":
			side = "old"
			blocks = oldBlocks
		}
		for _, b := range blocks {
			kind := compare.KindAdded
			if side == "old" {
				kind = compare.KindRemoved
			}
			fr.blocks = append(fr.blocks, diffFor(b, nil, kind, name, 0, []string{"counterpart could not be analysed: " + fr.err}))
		}
		fr.reports = append(fr.reports, reportFor(j, len(oldBlocks), len(newBlocks), fr.blocks))
		return fr
	}

	var results []compare.BlockResult
	if j.old != nil && j.new != nil {
		results = compare.MatchFunction(j.old.Name, j.new.Name, oldBlocks, newBlocks)
	} else if j.old != nil {
		for _, b := range oldBlocks {
			results = append(results, compare.BlockResult{Kind: compare.KindRemoved, Func: j.old.Name, Block: b.Block, Canon: b})
		}
	} else {
		for _, b := range newBlocks {
			results = append(results, compare.BlockResult{Kind: compare.KindAdded, Func: j.new.Name, Block: b.Block, Canon: b})
		}
	}

	equal := 0
	for _, r := range results {
		switch r.Kind {
		case compare.KindAdded:
			fr.blocks = append(fr.blocks, diffFor(r.Canon, nil, compare.KindAdded, r.Func, r.Confidence, r.Reasons))
		case compare.KindRemoved:
			fr.blocks = append(fr.blocks, diffFor(r.Canon, nil, compare.KindRemoved, r.Func, r.Confidence, r.Reasons))
		case compare.KindChanged:
			reasons := r.Reasons
			conf := r.Confidence
			if j.uncertain {
				conf = minInt(conf, 50)
				reasons = append(reasons, "function alignment itself is uncertain")
			}
			d := diffFor(r.Canon, r.Counterpart, compare.KindChanged, name, conf, reasons)
			d.ChangedInstructions = r.ChangedInsns
			fr.blocks = append(fr.blocks, d)
		case compare.KindUncertain:
			reasons := r.Reasons
			conf := r.Confidence
			if j.uncertain {
				conf = minInt(conf, 50)
				reasons = append(reasons, "function alignment itself is uncertain")
			}
			fr.blocks = append(fr.blocks, diffFor(r.Canon, r.Counterpart, compare.KindUncertain, name, conf, reasons))
		case compare.KindEqual:
			// Not reported; counts feed the function report only.
			equal++
		}
	}
	report := reportFor(j, len(oldBlocks), len(newBlocks), fr.blocks)
	report.Matched = equal
	fr.reports = append(fr.reports, report)
	return fr
}

func diffFor(b *compare.CanonBlock, counterpart *compare.CanonBlock, kind compare.Kind, function string, confidence int, reasons []string) Diff {
	d := Diff{
		Address:          fmt.Sprintf("%#x", primaryAddress(b, counterpart, kind)),
		InstructionCount: primaryCount(b, counterpart, kind),
		SHA256:           primaryHash(b, counterpart, kind),
		Function:         function,
		Kind:             string(kind),
		Confidence:       confidence,
		Reasons:          append([]string(nil), reasons...),
	}
	if counterpart != nil {
		d.OldAddress = fmt.Sprintf("%#x", b.Block.Address)
		d.NewAddress = fmt.Sprintf("%#x", counterpart.Block.Address)
		d.OldSHA256 = b.Strict
	}
	if kind == compare.KindRemoved {
		d.OldAddress = fmt.Sprintf("%#x", b.Block.Address)
		d.Address = d.OldAddress
	}
	if kind == compare.KindAdded {
		d.NewAddress = fmt.Sprintf("%#x", b.Block.Address)
	}
	return d
}

func primaryAddress(b, counterpart *compare.CanonBlock, kind compare.Kind) uint64 {
	if kind == compare.KindRemoved || counterpart == nil {
		return b.Block.Address
	}
	return counterpart.Block.Address
}

func primaryCount(b, counterpart *compare.CanonBlock, kind compare.Kind) int {
	if kind == compare.KindRemoved || counterpart == nil {
		return b.Block.InstructionCount()
	}
	return counterpart.Block.InstructionCount()
}

func primaryHash(b, counterpart *compare.CanonBlock, kind compare.Kind) string {
	if kind == compare.KindRemoved || counterpart == nil {
		return b.Strict
	}
	return counterpart.Strict
}

func reportFor(j job, oldBlocks, newBlocks int, diffs []Diff) FunctionReport {
	r := FunctionReport{OldBlocks: oldBlocks, NewBlocks: newBlocks}
	if j.old != nil {
		r.OldAddress = fmt.Sprintf("%#x", j.old.Address)
		r.Name = j.old.Name
	}
	if j.new != nil {
		r.NewAddress = fmt.Sprintf("%#x", j.new.Address)
		r.Name = j.new.Name
	}
	for _, d := range diffs {
		switch d.Kind {
		case string(compare.KindChanged):
			r.Changed++
		case string(compare.KindUncertain):
			r.Uncertain++
		}
	}
	switch {
	case j.uncertain:
		r.Status = "uncertain"
	case j.old != nil && j.new != nil:
		r.Status = "aligned"
	case j.old != nil:
		r.Status = "old-only"
	default:
		r.Status = "new-only"
	}
	return r
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func tagFile(side string, diags []elfscan.Diagnostic) []elfscan.Diagnostic {
	out := make([]elfscan.Diagnostic, 0, len(diags))
	for _, d := range diags {
		d.Message = side + " image: " + d.Message
		out = append(out, d)
	}
	return out
}

func severityRank(s elfscan.Severity) int {
	switch s {
	case elfscan.SeverityError:
		return 0
	case elfscan.SeverityWarning:
		return 1
	default:
		return 2
	}
}

func parseAddr(s string) uint64 {
	v, err := strconv.ParseUint(strings.TrimPrefix(s, "0x"), 16, 64)
	if err != nil {
		return 0
	}
	return v
}
