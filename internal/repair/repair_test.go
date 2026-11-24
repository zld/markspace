package repair

import (
	"testing"

	"github.com/example/tsrepair/internal/model"
	"github.com/example/tsrepair/internal/timebase"
	"github.com/example/tsrepair/internal/ts"
)

func pkt(pid uint16, cc byte, pusi bool) *ts.Packet {
	p, err := ts.Parse(ts.BuildPacket(ts.BuildOpts{
		PID: pid, CC: cc, PUSI: pusi,
		AdaptationControl: 1, Payload: []byte{0xAA}}), 0)
	if err != nil {
		panic(err)
	}
	return p
}

func TestContinuityNormalGapDuplicate(t *testing.T) {
	c := NewContinuityChecker()
	mk := func(cc byte, off int64) *ts.Packet {
		p := pkt(0x100, cc, false)
		p.ByteOffset = off
		return p
	}
	if r, _ := c.Check(mk(0, 0)); r != CheckPass {
		t.Fatal("first packet must pass")
	}
	if r, _ := c.Check(mk(1, 188)); r != CheckPass {
		t.Fatal("increment must pass")
	}
	if r, _ := c.Check(mk(1, 376)); r != CheckDuplicate {
		t.Fatal("same CC must be duplicate")
	}
	r, ev := c.Check(mk(5, 564))
	// CC 1 -> 5 skips 2,3,4: four missing packets.
	if r != CheckGap || ev == nil || ev.Missing != 4 {
		t.Fatalf("gap detection wrong: r=%d ev=%+v", r, ev)
	}
	if ev.ByteRange != [2]int64{188, 564} {
		t.Fatalf("byte range wrong: %+v", ev.ByteRange)
	}
	st := c.Stats()
	if len(st.Duplicates) != 1 || len(st.Gaps) != 1 {
		t.Fatalf("stats wrong: %+v", st)
	}
}

func TestContinuityWrapsAt15AndNullSkipped(t *testing.T) {
	c := NewContinuityChecker()
	p15 := pkt(0x100, 0x0F, false)
	p0 := pkt(0x100, 0x00, false)
	c.Check(p15)
	if r, _ := c.Check(p0); r != CheckPass {
		t.Fatal("CC must wrap 0x0F -> 0x00")
	}
	null, _ := ts.Parse(ts.BuildPacket(ts.BuildOpts{PID: ts.PIDNull, CC: 9}), 0)
	if r, _ := c.Check(null); r != CheckPass || c.Stats().NullSkipped != 1 {
		t.Fatal("null packets are not continuity checked")
	}
}

func TestDiscontinuityIndicatorResetsExpectation(t *testing.T) {
	c := NewContinuityChecker()
	c.Check(pkt(0x100, 3, false))
	raw := ts.BuildPacket(ts.BuildOpts{
		PID: 0x100, CC: 9, AdaptationControl: 2, Discontinuity: true})
	p, _ := ts.Parse(raw, 0)
	if r, _ := c.Check(p); r != CheckPass {
		t.Fatal("adaptation discontinuity should reset CC expectation")
	}
}

func TestPCRWrapKeepsRawAndCorrected(t *testing.T) {
	c := NewTimestampCorrector()
	mk := func(base uint64, off int64) *ts.Packet {
		raw := ts.BuildPacket(ts.BuildOpts{
			PID: 0x200, AdaptationControl: 2,
			WithPCR: true, PCRBase: base})
		p, _ := ts.Parse(raw, off)
		return p
	}
	if _, err := c.ObservePCR(mk(timebase.Mask33-100, 0)); err != nil {
		t.Fatal(err)
	}
	corr, err := c.ObservePCR(mk(50, 188))
	if err != nil {
		t.Fatalf("wrap must be repaired, got %v", err)
	}
	if corr.Raw != 50 || corr.Corrected != timebase.Mask33+50 || corr.Wraps != 1 {
		t.Fatalf("wrap correction wrong: %+v", corr)
	}
	if c.WrapCount() != 1 {
		t.Fatalf("wrap count=%d", c.WrapCount())
	}
}

func TestPCRRegressionIsRecoverable(t *testing.T) {
	c := NewTimestampCorrector()
	mk := func(base uint64) *ts.Packet {
		raw := ts.BuildPacket(ts.BuildOpts{
			PID: 0x200, AdaptationControl: 2, WithPCR: true, PCRBase: base})
		p, _ := ts.Parse(raw, 0)
		return p
	}
	c.ObservePCR(mk(9000))
	_, err := c.ObservePCR(mk(8000)) // small backward step, not a wrap
	if err == nil {
		t.Fatal("backwards PCR must be reported")
	}
	se := model.AsStreamError(err)
	if se == nil || se.Severity != model.SeverityRecoverable || se.Stage != model.StageTimebase {
		t.Fatalf("error classification wrong: %v", err)
	}
	if len(c.Regressions()) != 1 || c.Regressions()[0].Raw != 8000 {
		t.Fatal("regression must retain the original raw value")
	}
}

func TestPTSUnwrapPerTrackIndependent(t *testing.T) {
	c := NewTimestampCorrector()
	c1, _ := c.ObservePTS(0x100, 0, 10)
	c2, _ := c.ObservePTS(0x102, 0, 20)
	if c1.Corrected != 10 || c2.Corrected != 20 {
		t.Fatal("tracks must keep independent clocks")
	}
	nearWrap := timebase.Mask33 - 4
	if _, err := c.ObservePTS(0x100, 100, nearWrap); err != nil {
		t.Fatalf("seed near-wrap PTS: %v", err)
	}
	wrap, err := c.ObservePTS(0x100, 188, 5) // small cross-boundary step
	if err != nil || wrap.Corrected != timebase.Mask33+5 {
		t.Fatalf("per-track PTS wrap wrong: %+v err=%v", wrap, err)
	}
}

func TestFatalMappingErrorShape(t *testing.T) {
	p := pkt(0x999, 0, false)
	err := FatalMappingError(p)
	if !model.IsFatal(err) || err.PID != 0x999 {
		t.Fatalf("mapping error shape wrong: %v", err)
	}
}
