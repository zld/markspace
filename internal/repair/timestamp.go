package repair

import (
	"github.com/example/tsrepair/internal/model"
	"github.com/example/tsrepair/internal/timebase"
	"github.com/example/tsrepair/internal/ts"
)

// TimestampCorrection records one unwrap or regression decision so the raw
// and corrected values can both be audited downstream.
type TimestampCorrection struct {
	PID        uint16
	Kind       string // "pcr" or "pts"
	Raw        uint64 // original 33-bit (or 42-bit PCR) value
	Corrected  uint64 // unwrapped absolute value
	Wraps      uint64
	Offset     int64
	Regression bool
}

// TimestampCorrector maintains one unwrap clock per PID for PCR and PTS.
type TimestampCorrector struct {
	pcrClocks map[uint16]*timebase.Clock33
	ptsClocks map[uint16]*timebase.Clock33
	history   []TimestampCorrection
	// Regressions are recoverable faults: the offending packet keeps its raw
	// value but the timeline is not advanced.
	regressions []TimestampCorrection
}

// NewTimestampCorrector returns an empty corrector.
func NewTimestampCorrector() *TimestampCorrector {
	return &TimestampCorrector{
		pcrClocks: map[uint16]*timebase.Clock33{},
		ptsClocks: map[uint16]*timebase.Clock33{},
	}
}

func (c *TimestampCorrector) pcrClock(pid uint16) *timebase.Clock33 {
	clk := c.pcrClocks[pid]
	if clk == nil {
		clk = &timebase.Clock33{}
		c.pcrClocks[pid] = clk
	}
	return clk
}

func (c *TimestampCorrector) ptsClock(pid uint16) *timebase.Clock33 {
	clk := c.ptsClocks[pid]
	if clk == nil {
		clk = &timebase.Clock33{}
		c.ptsClocks[pid] = clk
	}
	return clk
}

// ObservePCR unwraps the PCR of a packet carrying one. A backwards move that
// cannot be explained by a 33-bit wrap is returned as a recoverable
// StreamError; the caller may skip the packet while processing continues.
func (c *TimestampCorrector) ObservePCR(p *ts.Packet) (*TimestampCorrection, *model.StreamError) {
	if p.Adaptation == nil || !p.Adaptation.HasPCR {
		return nil, nil
	}
	clk := c.pcrClock(p.PID)
	raw := p.Adaptation.PCRBase
	reg := false
	if prev, ok := clk.LastRaw(); ok && timebase.Regression(prev, raw) {
		reg = true
		c.regressions = append(c.regressions, TimestampCorrection{
			PID: p.PID, Kind: "pcr", Raw: raw,
			Corrected: clkAbsolute(clk), Wraps: clk.Wraps(),
			Offset: p.ByteOffset, Regression: true})
		return nil, model.New(model.StageTimebase, model.SeverityRecoverable,
			"PCR moved backwards without a 33-bit wrap").
			Wrap(model.ErrTimeRegression).At(p.PID, p.ByteOffset)
	}
	abs := clk.Unwrap(raw)
	corr := TimestampCorrection{
		PID: p.PID, Kind: "pcr", Raw: raw, Corrected: abs,
		Wraps: clk.Wraps(), Offset: p.ByteOffset, Regression: reg}
	c.history = append(c.history, corr)
	return &corr, nil
}

// ObservePTS unwraps a PTS sample extracted by the caller from a PES header.
func (c *TimestampCorrector) ObservePTS(pid uint16, offset int64, pts33 uint64) (*TimestampCorrection, *model.StreamError) {
	clk := c.ptsClock(pid)
	if prev, ok := clk.LastRaw(); ok && timebase.Regression(prev, pts33) {
		c.regressions = append(c.regressions, TimestampCorrection{
			PID: pid, Kind: "pts", Raw: pts33,
			Corrected: clkAbsolute(clk), Wraps: clk.Wraps(),
			Offset: offset, Regression: true})
		return nil, model.New(model.StageTimebase, model.SeverityRecoverable,
			"PTS moved backwards without a 33-bit wrap").
			Wrap(model.ErrTimeRegression).At(pid, offset)
	}
	abs := clk.Unwrap(pts33)
	corr := TimestampCorrection{
		PID: pid, Kind: "pts", Raw: pts33, Corrected: abs,
		Wraps: clk.Wraps(), Offset: offset}
	c.history = append(c.history, corr)
	return &corr, nil
}

// Corrections returns all successful unwrap decisions, in stream order.
func (c *TimestampCorrector) Corrections() []TimestampCorrection {
	return append([]TimestampCorrection(nil), c.history...)
}

// Regressions returns the rejected backwards samples.
func (c *TimestampCorrector) Regressions() []TimestampCorrection {
	return append([]TimestampCorrection(nil), c.regressions...)
}

// WrapCount returns the total PCR/PTS wraps observed across tracks.
func (c *TimestampCorrector) WrapCount() int {
	n := 0
	for _, clk := range c.pcrClocks {
		n += int(clk.Wraps())
	}
	for _, clk := range c.ptsClocks {
		n += int(clk.Wraps())
	}
	return n
}

func clkAbsolute(clk *timebase.Clock33) uint64 {
	if v, ok := clk.Absolute(); ok {
		return v
	}
	return 0
}
