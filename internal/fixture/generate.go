package fixture

import (
	"context"

	"rtpregress/internal/clock"
)

// Case names emitted by the generator.
const (
	CaseClean   = "clean"
	CaseWrap    = "pcr_wrap"
	CaseDrift   = "drift"
	CaseReorder = "reorder"
	CaseCCPSI   = "cc_psi"
	CaseTrunc   = "truncated"
	CaseDamaged = "damaged"
	CaseCancel  = "cancel"
)

const (
	audioSeconds = 8
	// one 1024-sample AAC frame at 48 kHz spans 1024/90000 s in 90 kHz ticks
	ticksPerAACFrame = int64(1024) * clock.RTPPacketRate / 48000 // 1920
	// PCR is emitted every 5 AAC frames. Its nominal 27 MHz step must represent
	// the same elapsed time as those 5 RTP frames so both clocks share one rate;
	// the optional ppm skew is then a pure rate scaling applied on top.
	pcrFramesPerPCR = 5
	pcrStep         = ticksPerAACFrame * pcrFramesPerPCR *
		(clock.PCRRate / clock.RTPPacketRate) / 1
)

// Config selects a fault scenario for BuildTrace.
type Config struct {
	Name string
	// PSIBump, when > 0, emits a second PMT with this version mid-stream.
	PSIBump uint8
	// DriftPPM skews PCR progression relative to the RTP clock when nonzero.
	DriftPPM int64
	// WrapPCR drives the PCR near its 42-bit period boundary to force a wrap.
	WrapPCR bool
	// Reorder/duplicate/loss mutate RTP record ordering.
	Reorder, Duplicate, Loss bool
	// CCJump injects a continuity-counter jump.
	CCJump bool
	// Truncate drops the final partial TS bytes.
	Truncate bool
}

// BuildTrace synthesizes one complete RTP trace for the requested scenario.
// Audio access units are produced once by ffmpeg and reused deterministically.
func BuildTrace(ctx context.Context, ffmpeg string, cfg Config) ([]byte, error) {
	frames, err := GenerateAAC(ctx, ffmpeg, audioSeconds)
	if err != nil {
		return nil, err
	}
	b := NewTSBuilder()
	rw := NewRTPWriter(0x1A2B3C4D, 0)
	// initial PAT/PMT occupy the first RTP packet
	if err := rw.PushTS([][]byte{b.AddPAT(0), b.AddPMT(0)}, 0); err != nil {
		return nil, err
	}

	// PCR schedule: start near the wrap boundary when requested, else at 0.
	// nominalPCR tracks the un-skewed 27 MHz position; the emitted PCR is a
	// constant-rate scaling of it so DriftPPM is a steady rate, not cumulative.
	nominalPCR := int64(0)
	if cfg.WrapPCR {
		nominalPCR = clock.PCRWrapTicks - pcrStep*2
	}
	driftNumerator := int64(1_000_000 + cfg.DriftPPM) // relative PCR rate
	var pts int64 = 0                                 // first PTS at the epoch
	half := len(frames) / 2
	for i, au := range frames {
		var round [][]byte
		// mid-stream PSI version bump
		if i == half && cfg.PSIBump > 0 {
			round = append(round, b.AddPATPacket(cfg.PSIBump), b.AddPMTPacket(cfg.PSIBump))
		}
		if i%5 == 0 {
			// constant ppm: skewed = nominal * (1e6+ppm)/1e6
			skewedPCR := nominalPCR * driftNumerator / 1_000_000
			round = append(round, b.AddPCR(skewedPCR, false))
			nominalPCR += pcrStep
		}
		if cfg.CCJump && i == half {
			b.InjectCCJump()
		}
		b.AddAACPES(pts, au)
		pts += ticksPerAACFrame
		round = append(round, b.Drain()...)
		if err := rw.PushTS(round, uint32(ticksPerAACFrame)); err != nil {
			return nil, err
		}
	}
	applyRTPMutations(rw, cfg)
	raw := rw.Frame()
	if cfg.Truncate {
		// Cut the trace so the final TS packet is a partial (non-188) tail:
		// first drop whole RTP records until within one record of the end,
		// then trim enough bytes to leave a sub-packet TS remainder.
		raw = truncateToPartialTSPacket(raw)
	}
	return raw, nil
}

// truncateToPartialTSPacket trims the final complete RTP record's TS payload
// so it ends in a partial (non-188-byte) TS packet while the record framing
// stays intact. The RTP layer still delivers the record; the aligned TS
// demuxer then counts the sub-packet tail as a truncation.
func truncateToPartialTSPacket(raw []byte) []byte {
	// walk framed records from the end, skipping the 4-byte magic
	pos := 4
	type span struct{ start, length int }
	var records []span
	for pos+4 <= len(raw) {
		n := int(uint32(raw[pos])<<24 | uint32(raw[pos+1])<<16 |
			uint32(raw[pos+2])<<8 | uint32(raw[pos+3]))
		if n <= 0 || pos+4+n > len(raw) {
			break
		}
		records = append(records, span{pos + 4, n})
		pos += 4 + n
	}
	if len(records) == 0 {
		return raw
	}
	last := records[len(records)-1]
	// shorten the record body by 70 bytes and rewrite its length prefix,
	// keeping enough of the RTP packet for pion to still parse it
	if last.length <= 24 {
		return raw
	}
	newLen := last.length - 70
	out := make([]byte, 0, last.start+newLen)
	out = append(out, raw[:last.start-4]...)
	out = append(out, byte(newLen>>24), byte(newLen>>16), byte(newLen>>8), byte(newLen))
	out = append(out, raw[last.start:last.start+newLen]...)
	return out
}

func applyRTPMutations(rw *RTPWriter, cfg Config) {
	// Apply loss first on a record that is NOT the final one, so the later
	// duplicate/reorder still has a surviving last record to act on.
	if cfg.Loss {
		if n := rw.Count(); n > 3 {
			rw.DropAt(n - 3)
		} else {
			rw.DropLast()
		}
	}
	if cfg.Reorder {
		rw.ReorderLastTwo()
	}
	if cfg.Duplicate {
		rw.DuplicateLast()
	}
}

// Cases is the ordered name -> config catalog emitted by -generate.
func Cases() map[string]Config {
	return map[string]Config{
		CaseClean:   {Name: CaseClean},
		CaseWrap:    {Name: CaseWrap, WrapPCR: true},
		CaseDrift:   {Name: CaseDrift, DriftPPM: 1200},
		CaseReorder: {Name: CaseReorder, Reorder: true, Duplicate: true, Loss: true},
		CaseCCPSI:   {Name: CaseCCPSI, CCJump: true, PSIBump: 1},
		CaseTrunc:   {Name: CaseTrunc, Truncate: true},
		// damaged combines every recoverable fault class at once.
		CaseDamaged: {
			Name: CaseDamaged, CCJump: true, PSIBump: 2,
			Reorder: true, Duplicate: true, Loss: true, Truncate: true,
		},
		// cancel is structurally clean; cancellation is driven at read time.
		CaseCancel: {Name: CaseCancel},
	}
}

// OrderedCaseNames returns the catalog keys in a stable order.
func OrderedCaseNames() []string {
	return []string{
		CaseClean, CaseWrap, CaseDrift, CaseReorder,
		CaseCCPSI, CaseTrunc, CaseDamaged, CaseCancel,
	}
}
