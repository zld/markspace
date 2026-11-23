package probe

import (
	"github.com/pion/rtp"

	"github.com/broadcast/mediad/internal/model"
)

// SequenceAudit verifies monotonic sample numbering across the 16-bit
// wraparound boundary using the pion/rtp fixed sequencer as the reference
// generator. MP4 sample tables are 32-bit, but delivery pipelines that feed
// RTP must keep the low 16 bits gap-free; this makes that invariant explicit
// and reusable instead of hand-rolling wrap arithmetic.
type SequenceAudit struct {
	sequencer rtp.Sequencer
	rollovers uint64
}

// NewSequenceAudit starts the reference sequencer at the given 16-bit value.
func NewSequenceAudit(start uint16) *SequenceAudit {
	return &SequenceAudit{sequencer: rtp.NewFixedSequencer(start)}
}

// CheckOne compares the next expected 16-bit sequence number against the
// observed sample number. A mismatch is reported as an out-of-bounds style
// defect; rollovers are counted from the sequencer rather than assumed.
func (a *SequenceAudit) CheckOne(trackID uint32, sampleNumber uint32, errs *model.ErrorSet) {
	want := a.sequencer.NextSequenceNumber()
	got := uint16(sampleNumber)
	if want != got {
		errs.Add(model.MediaError{
			Kind:       model.KindSampleOutOfBounds,
			TrackID:    trackID,
			SampleIndex: int(sampleNumber) - 1,
			Detail: "16-bit sequence discontinuity: expected " +
				u16str(want) + " got " + u16str(got),
		})
	}
	a.rollovers = a.sequencer.RollOverCount()
}

// Rollovers returns how many times the reference counter crossed 65535->0.
func (a *SequenceAudit) Rollovers() uint64 { return a.rollovers }

func u16str(v uint16) string {
	const digits = "0123456789"
	if v == 0 {
		return "0"
	}
	var buf [5]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = digits[v%10]
		v /= 10
	}
	return string(buf[i:])
}

// AuditSampleNumbers runs the wraparound-aware audit over one track's sample
// numbering (1-based stss/stsz order). It is intentionally separate from the
// rational timeline checks because sequence and time defects have different
// recovery semantics.
func AuditSampleNumbers(t *model.Track, errs *model.ErrorSet) *SequenceAudit {
	if len(t.Samples) == 0 {
		return NewSequenceAudit(0)
	}
	// pion's fixed sequencer returns the seed on its first
	// NextSequenceNumber call, so seed it with the first observed 16-bit
	// sample number; subsequent calls increment (with 16-bit wraparound).
	first := uint16(t.Samples[0].Index + 1)
	audit := NewSequenceAudit(first)
	for _, s := range t.Samples {
		audit.CheckOne(t.ID, uint32(s.Index+1), errs)
	}
	return audit
}
