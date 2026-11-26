// Package engine wires the EBML, timeline, audio, cache and output
// subsystems into one bounded, restartable processing pipeline. It enforces
// the quality-first budget: candidates are measured, not assumed, and the
// smallest passing candidate wins.
package engine

import (
	"mediatrace/internal/audio"
	"mediatrace/internal/ebml"
)

// Exit codes are part of the CLI contract.
const (
	ExitOK            = 0
	ExitInputCorrupt  = 2
	ExitQualityBudget = 3
	ExitVerifyOrWrite = 4
)

// BudgetBitrates are the three AAC ladders tried in order. The engine keeps
// every passing candidate and selects the smallest at the end, so quality —
// not size — decides which candidates remain eligible.
var BudgetBitrates = []int{128, 96, 64}

// Plan is the fully resolved work description for one input file.
type Plan struct {
	InputPath    string
	EventsPath   string
	CacheDir     string
	OutputDir    string
	BudgetPasses int
	Limits       ebml.Limits
	MaxErrorNS   int64
	// MaxDecodeSeconds bounds FFmpeg decode/encode wall time per track.
	MaxDecodeSeconds int
}

// DefaultPlan returns the documented processing ceilings.
func DefaultPlan() Plan {
	return Plan{
		BudgetPasses:     3,
		Limits:           ebml.DefaultLimits,
		MaxErrorNS:       1_000_000,
		MaxDecodeSeconds: 300,
	}
}

// ladder returns the bitrate ladder allowed by the pass budget.
func (p Plan) ladder() []int {
	if p.BudgetPasses <= 0 {
		return nil
	}
	if p.BudgetPasses >= len(BudgetBitrates) {
		return BudgetBitrates
	}
	return BudgetBitrates[:p.BudgetPasses]
}

// candidate is one encoded AAC file plus its measured quality and size.
type candidate struct {
	bitrateKbps int
	path        string
	stats       audio.LoudnessStats
	sizeBytes   int64
}

// chooseSmallestPassing applies the quality-first rule: discard every
// candidate that fails the loudness/peak gate, then return the smallest
// survivor. A nil result means the budget was exhausted without quality.
func chooseSmallestPassing(cs []candidate) *candidate {
	var best *candidate
	for i := range cs {
		c := cs[i]
		if !c.stats.PassesGate() {
			continue
		}
		if best == nil || c.sizeBytes < best.sizeBytes {
			best = &c
		}
	}
	return best
}
