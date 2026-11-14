package compare

import (
	"fmt"
	"sort"

	"bbdiff/internal/cfg"
	"bbdiff/internal/elfscan"
)

// Kind is the classification of a block in the diff.
type Kind string

const (
	KindAdded     Kind = "added"
	KindRemoved   Kind = "removed"
	KindChanged   Kind = "changed"
	KindUncertain Kind = "uncertain"
	KindEqual     Kind = "equal"
)

// BlockResult is the comparison outcome for one block.
type BlockResult struct {
	Kind  Kind
	Func  string
	Block *cfg.Block
	Canon *CanonBlock
	// Counterpart is the matched block on the other side, if any.
	Counterpart *CanonBlock
	// CounterpartFunc names the function owning the counterpart.
	CounterpartFunc string
	// Confidence is 0..100; lower values mean the match relied on weak
	// evidence or unresolved address fields.
	Confidence int
	// Reasons explains the classification.
	Reasons []string
	// ChangedInsns is the number of instruction positions whose canonical
	// form differs from the counterpart.
	ChangedInsns int
}

// FunctionDiff aggregates block results for one aligned function.
type FunctionDiff struct {
	Name    string
	OldName string
	NewName string
	Blocks  []BlockResult
	// Diagnostics from analysis of this function.
	Diagnostics []elfscan.Diagnostic
	// Error is set when the function could not be analysed (decode failure,
	// cancellation).  Other functions are unaffected.
	Error string
}

// MatchFunction compares the blocks of two aligned functions.
//
// Matching proceeds in two phases:
//  1. exact strict-hash matching with address tie-breaking, then exact
//     loose-hash matching (identical except for unresolved addresses, which is
//     reported with reduced confidence);
//  2. structural matching of leftovers using instruction shape, block size,
//     and successor/predecessor relationships within the function.
func MatchFunction(oldName, newName string, oldBlocks, newBlocks []*CanonBlock) []BlockResult {
	var results []BlockResult
	matchedOld := make(map[int]bool)
	matchedNew := make(map[int]bool)

	oldEdges := successorKeys(oldBlocks)
	newEdges := successorKeys(newBlocks)
	oldPred, newPred := predecessorHashes(oldBlocks, oldEdges), predecessorHashes(newBlocks, newEdges)

	// Phase 1a: identical strict hash.  When several blocks share a hash,
	// pair them in address order; the earliest old block gets the earliest
	// new block with the same relative position, which keeps the assignment
	// deterministic and stable across address shifts.
	pairExact(oldBlocks, newBlocks, matchedOld, matchedNew, &results, false)
	// Phase 1b: identical loose hash.
	pairExact(oldBlocks, newBlocks, matchedOld, matchedNew, &results, true)

	// Phase 2: structural matching of the remaining blocks.
	matchStructural(oldBlocks, newBlocks, oldPred, newPred, matchedOld, matchedNew, &results)

	// Leftovers.
	for i, ob := range oldBlocks {
		if matchedOld[i] {
			continue
		}
		results = append(results, BlockResult{
			Kind:       KindRemoved,
			Func:       oldName,
			Block:      ob.Block,
			Canon:      ob,
			Confidence: 100,
			Reasons:    []string{"no counterpart in the new build"},
		})
	}
	for i, nb := range newBlocks {
		if matchedNew[i] {
			continue
		}
		results = append(results, BlockResult{
			Kind:       KindAdded,
			Func:       newName,
			Block:      nb.Block,
			Canon:      nb,
			Confidence: 100,
			Reasons:    []string{"no counterpart in the old build"},
		})
	}

	sortResults(&results)
	return results
}

func pairExact(oldBlocks, newBlocks []*CanonBlock, matchedOld, matchedNew map[int]bool, results *[]BlockResult, loose bool) {
	hashOf := func(b *CanonBlock) string {
		if loose {
			return b.Loose
		}
		return b.Strict
	}
	newByHash := make(map[string][]int)
	for j, nb := range newBlocks {
		if matchedNew[j] {
			continue
		}
		h := hashOf(nb)
		newByHash[h] = append(newByHash[h], j)
	}
	for i, ob := range oldBlocks {
		if matchedOld[i] {
			continue
		}
		cands, ok := newByHash[hashOf(ob)]
		if !ok {
			continue
		}
		// Choose the unmatched candidate with the closest address.
		best := -1
		bestDist := uint64(1<<63 - 1)
		for _, j := range cands {
			if matchedNew[j] {
				continue
			}
			dist := absDiff(ob.Block.Address, newBlocks[j].Block.Address)
			if best < 0 || dist < bestDist || (dist == bestDist && newBlocks[j].Block.Address < newBlocks[best].Block.Address) {
				best = j
				bestDist = dist
			}
		}
		if best < 0 {
			continue
		}
		matchedOld[i] = true
		matchedNew[best] = true
		res := BlockResult{
			Kind:        KindEqual,
			Block:       ob.Block,
			Canon:       ob,
			Counterpart: newBlocks[best],
			Confidence:  100,
		}
		if loose && ob.Strict != newBlocks[best].Strict {
			res.Kind = KindUncertain
			res.Confidence = 55
			res.Reasons = append(res.Reasons, "blocks differ only in unresolved or relocated address fields")
		}
		if ob.Unresolved > 0 || newBlocks[best].Unresolved > 0 {
			if res.Confidence > 70 {
				res.Confidence = 70
			}
			res.Reasons = append(res.Reasons, fmt.Sprintf("%d/%d unresolved relocations in the block", ob.Unresolved, newBlocks[best].Unresolved))
		}
		*results = append(*results, res)
	}
}

func matchStructural(oldBlocks, newBlocks []*CanonBlock, oldPred, newPred map[uint64][]string, matchedOld, matchedNew map[int]bool, results *[]BlockResult) {
	type pair struct {
		i, j  int
		score int
	}
	var pairs []pair
	for i, ob := range oldBlocks {
		if matchedOld[i] {
			continue
		}
		for j, nb := range newBlocks {
			if matchedNew[j] {
				continue
			}
			s := structuralScore(ob, nb) + adjacencyScore(oldPred[ob.Block.Address], newPred[nb.Block.Address])
			if s <= 0 {
				continue
			}
			pairs = append(pairs, pair{i: i, j: j, score: s})
		}
	}
	sort.Slice(pairs, func(a, b int) bool {
		if pairs[a].score != pairs[b].score {
			return pairs[a].score > pairs[b].score
		}
		if pairs[a].i != pairs[b].i {
			return pairs[a].i < pairs[b].i
		}
		return pairs[a].j < pairs[b].j
	})

	// Only accept pairs whose score is unique in practice; the greedy order
	// plus the cross-check below prevents a chain of weak matches.
	for _, p := range pairs {
		if matchedOld[p.i] || matchedNew[p.j] {
			continue
		}
		ob, nb := oldBlocks[p.i], newBlocks[p.j]
		changed := changedPositions(ob, nb)
		if changed < 0 {
			continue
		}
		matchedOld[p.i] = true
		matchedNew[p.j] = true
		res := BlockResult{
			Kind:         KindChanged,
			Block:        ob.Block,
			Canon:        ob,
			Counterpart:  nb,
			ChangedInsns: changed,
			Confidence:   confidenceOf(p.score, ob, nb),
		}
		res.Reasons = append(res.Reasons, fmt.Sprintf("matched structurally (score %d): %d of %d instructions changed", p.score, changed, maxInt(len(ob.Shapes), len(nb.Shapes))))
		if ob.Unresolved > 0 || nb.Unresolved > 0 {
			res.Kind = KindUncertain
			res.Reasons = append(res.Reasons, "block contains unresolved addresses, so the match cannot be confirmed")
		}
		*results = append(*results, res)
	}
}

// adjacencyScore rewards a candidate whose predecessors have identical
// canonical hashes.  This uses the control-flow graph, not just this block's
// bytes, to tell apart two structurally similar blocks (the arms of an
// if/else, for example) that differ only in where they came from.
func adjacencyScore(oldPreds, newPreds []string) int {
	if len(oldPreds) == 0 && len(newPreds) == 0 {
		return 0
	}
	if len(oldPreds) != len(newPreds) {
		return 0
	}
	for i := range oldPreds {
		if oldPreds[i] != newPreds[i] {
			return 0
		}
	}
	return 4
}

// structuralScore returns 0 when the blocks are not plausibly related.
func structuralScore(old, new *CanonBlock) int {
	shapeSim := shapeSimilarity(old.Shapes, new.Shapes)
	if shapeSim == 0 {
		return 0
	}
	score := shapeSim * 10
	// Size proximity.
	diff := absDiffInt(len(old.Shapes), len(new.Shapes))
	if diff == 0 {
		score += 6
	} else if diff <= 2 {
		score += 2
	} else {
		return 0
	}
	// Same terminator kind keeps loop bodies together.
	if old.Block.Terminator == new.Block.Terminator {
		score += 3
	}
	// Identical strict hash already consumed in phase 1; anything here is a
	// real change, so require a measurable shape similarity.
	if score < 15 {
		return 0
	}
	return score
}

// shapeSimilarity is the fraction of LCS on the shape sequences, scaled to
// 0..10.  Sequences longer than maxShapeLength are compared on a bounded
// prefix/suffix window to keep the cost quadratic but small.
func shapeSimilarity(a, b []string) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	la, lb := len(a), len(b)
	limit := maxShapeLength
	if la > limit {
		a = a[:limit]
		la = limit
	}
	if lb > limit {
		b = b[:limit]
		lb = limit
	}
	dp := make([][]int, la+1)
	for i := range dp {
		dp[i] = make([]int, lb+1)
	}
	for i := 1; i <= la; i++ {
		for j := 1; j <= lb; j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}
	lcs := dp[la][lb]
	largest := la
	if lb > largest {
		largest = lb
	}
	// Require at least half of the larger block in common shape terms.
	if lcs*2 < largest {
		return 0
	}
	return lcs * 10 / largest
}

const maxShapeLength = 256

// changedPositions compares aligned instruction positions and returns -1 when
// the blocks cannot be aligned.  It uses the same LCS alignment as the shape
// similarity but on full canonical encodings.
func changedPositions(old, new *CanonBlock) int {
	limit := maxShapeLength
	oa, ob := old.Insns, new.Insns
	if len(oa) > limit {
		oa = oa[:limit]
	}
	if len(ob) > limit {
		ob = ob[:limit]
	}
	la, lb := len(oa), len(ob)
	eq := func(i, j int) bool {
		return oa[i].Text == ob[j].Text && bytesEq(oa[i].Encoding, ob[j].Encoding) && tokensEq(oa[i].Tokens, ob[j].Tokens)
	}
	// LCS table.
	dp := make([][]int, la+1)
	for i := range dp {
		dp[i] = make([]int, lb+1)
	}
	for i := 1; i <= la; i++ {
		for j := 1; j <= lb; j++ {
			if eq(i-1, j-1) {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}
	common := dp[la][lb]
	largest := la
	if lb > largest {
		largest = lb
	}
	if largest == 0 {
		return 0
	}
	return largest - common
}

func bytesEq(a, b []byte) bool {
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

func tokensEq(a, b []string) bool {
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

func confidenceOf(score int, old, new *CanonBlock) int {
	c := score * 2
	if c > 100 {
		c = 100
	}
	if c < 40 {
		c = 40
	}
	if old.Unresolved > 0 || new.Unresolved > 0 {
		c -= 20
	}
	if c < 10 {
		c = 10
	}
	return c
}

// successorKeys returns, for each block address, the addresses of the blocks
// its intra-function edges reach.  Only edges whose target is itself a known
// block are kept.
func successorKeys(blocks []*CanonBlock) map[uint64][]uint64 {
	known := make(map[uint64]bool, len(blocks))
	for _, b := range blocks {
		known[b.Block.Address] = true
	}
	out := make(map[uint64][]uint64, len(blocks))
	for _, b := range blocks {
		var succ []uint64
		for _, e := range b.Block.Edges {
			if e.Kind == cfg.EdgeCall || e.Kind == cfg.EdgeIndirect {
				continue
			}
			if known[e.Target] {
				succ = append(succ, e.Target)
			}
		}
		sort.Slice(succ, func(i, j int) bool { return succ[i] < succ[j] })
		out[b.Block.Address] = dedupeAddrs(succ)
	}
	return out
}

// predecessorHashes returns, for each block address, the canonical hashes of
// its intra-function predecessors, sorted.  Two blocks with identical
// predecessor hash sets occupy the same CFG position relative to already
// canonicalised code, which is evidence that they are the same block.
func predecessorHashes(blocks []*CanonBlock, edges map[uint64][]uint64) map[uint64][]string {
	hashByAddr := make(map[uint64]string, len(blocks))
	for _, b := range blocks {
		hashByAddr[b.Block.Address] = b.Strict
	}
	preds := predecessorSets(edges)
	out := make(map[uint64][]string, len(preds))
	for addr, froms := range preds {
		hashes := make([]string, 0, len(froms))
		for _, from := range froms {
			hashes = append(hashes, hashByAddr[from])
		}
		sort.Strings(hashes)
		out[addr] = hashes
	}
	return out
}

// predecessorSets inverts the successor map.
func predecessorSets(edges map[uint64][]uint64) map[uint64][]uint64 {
	preds := make(map[uint64][]uint64, len(edges))
	for from, tos := range edges {
		for _, to := range tos {
			preds[to] = append(preds[to], from)
		}
	}
	for addr := range preds {
		sort.Slice(preds[addr], func(i, j int) bool { return preds[addr][i] < preds[addr][j] })
		preds[addr] = dedupeAddrs(preds[addr])
	}
	return preds
}

func dedupeAddrs(a []uint64) []uint64 {
	out := a[:0]
	for i, v := range a {
		if i > 0 && v == a[i-1] {
			continue
		}
		out = append(out, v)
	}
	return out
}

func sortResults(results *[]BlockResult) {
	sort.SliceStable(*results, func(i, j int) bool {
		ai := (*results)[i].Block.Address
		aj := (*results)[j].Block.Address
		if ai != aj {
			return ai < aj
		}
		return string((*results)[i].Kind) < string((*results)[j].Kind)
	})
}

func absDiff(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return b - a
}

func absDiffInt(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
