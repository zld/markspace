// Package align builds the function correspondence between two ELF images.
//
// Alignment is symbol driven.  Function symbols sharing an address and section
// are folded into one Function with an alias set, so an alias that was renamed
// between builds is matched through any surviving common name.  A name that
// occurs more than once in either image is disambiguated using its section,
// size, and the set of symbols it calls; when that evidence leaves more than
// one plausible assignment the pair is reported as uncertain rather than
// guessed.
package align

import (
	"debug/elf"
	"fmt"
	"sort"

	"bbdiff/internal/elfscan"
)

// Function is one code range identified by one or more symbol names.
type Function struct {
	// Name is the primary name chosen deterministically from Aliases.
	Name string
	// Aliases lists every symbol name sharing this address and section, sorted
	// by length and then lexicographically.
	Aliases    []string
	Address    uint64
	Size       uint64
	Section    string
	SectionIdx int
	// Calls is the sorted set of function symbols referenced by relocations
	// that fall inside this function.  It is evidence for disambiguation, not
	// a disassembly of the calls.
	Calls []string
}

// Pair is a proposed old/new function correspondence.
type Pair struct {
	Old *Function
	New *Function
}

// Label renders "old -> new" for diagnostics.
func (p Pair) Label() string {
	oldName, newName := "<nil>", "<nil>"
	if p.Old != nil {
		oldName = p.Old.Name
	}
	if p.New != nil {
		newName = p.New.Name
	}
	return oldName + " -> " + newName
}

// Result is the complete alignment outcome.
type Result struct {
	// Pairs are confidently aligned functions.
	Pairs []Pair
	// Uncertain pairs could not be resolved to a unique candidate.
	Uncertain []Pair
	// Removed are old functions with no counterpart.
	Removed []*Function
	// Added are new functions with no counterpart.
	Added []*Function
	// Diagnostics explain every uncertain decision.
	Diagnostics []elfscan.Diagnostic
}

// Align matches the functions of oldFile and newFile.
func Align(oldFile, newFile *elfscan.File) Result {
	oldFns := Collect(oldFile)
	newFns := Collect(newFile)

	oldByName := indexAliases(oldFns)
	newByName := indexAliases(newFns)

	var res Result
	matchedOld := make(map[*Function]bool)
	matchedNew := make(map[*Function]bool)

	for _, name := range unionNames(oldByName, newByName) {
		og := oldByName[name]
		ng := newByName[name]
		if len(og) == 0 || len(ng) == 0 {
			continue
		}
		if len(og) == 1 && len(ng) == 1 {
			if !matchedOld[og[0]] && !matchedNew[ng[0]] {
				res.Pairs = append(res.Pairs, Pair{Old: og[0], New: ng[0]})
				matchedOld[og[0]] = true
				matchedNew[ng[0]] = true
			}
			continue
		}
		pairs, uncertainReason := disambiguate(name, og, ng)
		if uncertainReason != "" {
			res.Diagnostics = append(res.Diagnostics, elfscan.Diagnostic{
				Severity: elfscan.SeverityWarning,
				Code:     "ambiguous-symbol",
				Message:  fmt.Sprintf("symbol %q: %s", name, uncertainReason),
			})
			for _, p := range pairs {
				if !matchedOld[p.Old] && !matchedNew[p.New] {
					res.Uncertain = append(res.Uncertain, p)
					matchedOld[p.Old] = true
					matchedNew[p.New] = true
				}
			}
			continue
		}
		for _, p := range pairs {
			if !matchedOld[p.Old] && !matchedNew[p.New] {
				res.Pairs = append(res.Pairs, p)
				matchedOld[p.Old] = true
				matchedNew[p.New] = true
			}
		}
	}

	for _, fn := range oldFns {
		if !matchedOld[fn] {
			res.Removed = append(res.Removed, fn)
		}
	}
	for _, fn := range newFns {
		if !matchedNew[fn] {
			res.Added = append(res.Added, fn)
		}
	}

	sortFunctions(res.Removed)
	sortFunctions(res.Added)
	sortPairs(res.Pairs)
	sortPairs(res.Uncertain)
	return res
}

const symTypeFunc = elf.STT_FUNC

// Collect extracts function-like symbols from a file: defined STT_FUNC symbols
// with a non-zero size, inside an executable section, whose address range fits
// the section.
//
// Symbols sharing (section, address) are folded into a single Function.  The
// primary name is the alias with the shortest length, then the smallest
// lexicographic order, so the choice is stable across runs.
func Collect(f *elfscan.File) []*Function {
	type key struct {
		section int
		address uint64
	}
	byKey := make(map[key]*Function)
	var order []*Function

	for i := range f.Symbols {
		s := &f.Symbols[i]
		if s.Type != symTypeFunc || !s.Defined || s.OutOfBounds || s.Size == 0 {
			continue
		}
		if int(s.SectionIndex) >= len(f.Sections) {
			continue
		}
		sec := f.Sections[s.SectionIndex]
		if !sec.Executable {
			continue
		}
		if s.Value < sec.Addr || s.Value >= sec.Addr+sec.Size {
			continue
		}
		if s.Size > sec.Addr+sec.Size-s.Value {
			continue
		}
		k := key{section: sec.Index, address: s.Value}
		fn, ok := byKey[k]
		if !ok {
			fn = &Function{
				Address:    s.Value,
				Size:       s.Size,
				Section:    sec.Name,
				SectionIdx: sec.Index,
			}
			byKey[k] = fn
			order = append(order, fn)
		}
		if s.Size > fn.Size {
			fn.Size = s.Size
		}
		if s.Name != "" {
			fn.Aliases = append(fn.Aliases, s.Name)
		}
	}

	calls := callReferences(f, order)
	out := make([]*Function, 0, len(order))
	for _, fn := range order {
		if len(fn.Aliases) == 0 {
			continue
		}
		sortAliases(fn.Aliases)
		fn.Name = fn.Aliases[0]
		fn.Calls = calls[fn]
		out = append(out, fn)
	}
	sortFunctions(out)
	return out
}

func sortAliases(names []string) {
	sort.Slice(names, func(i, j int) bool {
		if len(names[i]) != len(names[j]) {
			return len(names[i]) < len(names[j])
		}
		return names[i] < names[j]
	})
}

func sortFunctions(fns []*Function) {
	sort.SliceStable(fns, func(i, j int) bool {
		if fns[i].Address != fns[j].Address {
			return fns[i].Address < fns[j].Address
		}
		return fns[i].Name < fns[j].Name
	})
}

func sortPairs(pairs []Pair) {
	sort.SliceStable(pairs, func(i, j int) bool {
		return pairs[i].Old.Address < pairs[j].Old.Address
	})
}

func indexAliases(fns []*Function) map[string][]*Function {
	out := make(map[string][]*Function)
	for _, fn := range fns {
		for _, name := range fn.Aliases {
			out[name] = append(out[name], fn)
		}
	}
	for name := range out {
		out[name] = dedupe(out[name])
		sortFunctions(out[name])
	}
	return out
}

func unionNames(a, b map[string][]*Function) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(a)+len(b))
	for name := range a {
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	for name := range b {
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func dedupe(fns []*Function) []*Function {
	seen := make(map[*Function]bool, len(fns))
	out := make([]*Function, 0, len(fns))
	for _, fn := range fns {
		if !seen[fn] {
			seen[fn] = true
			out = append(out, fn)
		}
	}
	return out
}

// disambiguate resolves a name that occurs multiple times on at least one
// side.  It returns a complete assignment, or a reason explaining why the
// assignment is not unique (in which case every returned pair is uncertain).
//
// Scoring, in priority order:
//   - size equality (4 points)
//   - same section name (2 points)
//   - identical call-target sets (2 points)
//
// A candidate pair is accepted only when it is the unique maximum for both
// sides and its score is at least 6 (size plus one corroborating signal).
func disambiguate(name string, oldCands, newCands []*Function) ([]Pair, string) {
	// Reduce to candidate sets.
	og := dedupe(oldCands)
	ng := dedupe(newCands)

	if len(og) == 1 && len(ng) == 1 {
		return []Pair{{Old: og[0], New: ng[0]}}, ""
	}

	// Score matrix and per-row / per-column maxima.
	scores := make([][]int, len(og))
	for i, o := range og {
		scores[i] = make([]int, len(ng))
		for j, n := range ng {
			scores[i][j] = score(o, n)
		}
	}

	assignment := make([]int, len(og))
	for i := range assignment {
		assignment[i] = -1
	}
	used := make(map[int]bool)

	// Greedy assignment over descending scores, ties broken deterministically.
	type cell struct{ i, j, s int }
	var cells []cell
	for i := range og {
		for j := range ng {
			cells = append(cells, cell{i: i, j: j, s: scores[i][j]})
		}
	}
	sort.Slice(cells, func(a, b int) bool {
		if cells[a].s != cells[b].s {
			return cells[a].s > cells[b].s
		}
		if cells[a].i != cells[b].i {
			return cells[a].i < cells[b].i
		}
		return cells[a].j < cells[b].j
	})
	for _, c := range cells {
		if c.s < 6 {
			break
		}
		if assignment[c.i] >= 0 || used[c.j] {
			continue
		}
		assignment[c.i] = c.j
		used[c.j] = true
	}

	// Every old candidate needs an assignment, every new candidate must be
	// claimed, and each accepted pair must be the unique best for its row and
	// column.  Otherwise the whole name is uncertain.
	if len(og) != len(ng) {
		return uncertainAll(og, ng), fmt.Sprintf("%q has %d old and %d new definitions; cannot pair one-to-one", name, len(og), len(ng))
	}
	for i := range assignment {
		if assignment[i] < 0 {
			return uncertainAll(og, ng), fmt.Sprintf("%q has no unique counterpart using size, section and call evidence", name)
		}
		j := assignment[i]
		// Unique maximum in row i.
		for jj := range ng {
			if jj != j && scores[i][jj] >= scores[i][j] {
				return uncertainAll(og, ng), fmt.Sprintf("%q has several equally good candidates", name)
			}
		}
		// Unique maximum in column j.
		for ii := range og {
			if ii != i && scores[ii][j] >= scores[i][j] {
				return uncertainAll(og, ng), fmt.Sprintf("%q has several equally good candidates", name)
			}
		}
	}

	pairs := make([]Pair, 0, len(og))
	for i := range assignment {
		pairs = append(pairs, Pair{Old: og[i], New: ng[assignment[i]]})
	}
	return pairs, ""
}

func uncertainAll(og, ng []*Function) []Pair {
	n := len(og)
	if len(ng) < n {
		n = len(ng)
	}
	out := make([]Pair, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, Pair{Old: og[i], New: ng[i]})
	}
	return out
}

func score(o, n *Function) int {
	s := 0
	if o.Size == n.Size {
		s += 4
	}
	if o.Section == n.Section {
		s += 2
	}
	if sameStrings(o.Calls, n.Calls) {
		s += 2
	}
	return s
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// callReferences computes, for each function, the sorted unique names of
// function symbols referenced by relocations inside the function's range.
func callReferences(f *elfscan.File, fns []*Function) map[*Function][]string {
	isFuncSymbol := make(map[string]bool)
	for i := range f.Symbols {
		s := &f.Symbols[i]
		if s.Type == symTypeFunc && s.Defined {
			isFuncSymbol[s.Name] = true
		}
	}
	out := make(map[*Function][]string, len(fns))
	for _, fn := range fns {
		seen := make(map[string]bool)
		var names []string
		for i := range f.Relocs {
			r := &f.Relocs[i]
			if r.Address < fn.Address || r.Address >= fn.Address+fn.Size {
				continue
			}
			if r.SymbolName == "" || !isFuncSymbol[r.SymbolName] || seen[r.SymbolName] {
				continue
			}
			seen[r.SymbolName] = true
			names = append(names, r.SymbolName)
		}
		sort.Strings(names)
		out[fn] = names
	}
	return out
}
