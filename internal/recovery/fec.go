// Package recovery implements single-packet loss recovery with one XOR
// redundancy packet per FEC group, as used in the local RTP fixtures.
//
// Invariant enforced here: a recovered event is only ever synthesized for a
// sequence that belongs to a gap explicitly declared by an upstream stage.
// Two missing members in one group cannot be solved by a single XOR packet and
// are reported unrecoverable.
package recovery

import (
	"fmt"

	"mediaqc/internal/model"
)

// Group describes one FEC protection group: the XOR of every member payload.
type Group struct {
	TrackID  string
	GroupID  uint64
	Members  []uint64 // member sequence numbers, in group order
	Parity   []byte   // XOR of all member payloads
}

// Stats counts recovery outcomes for the evidence report.
type Stats struct {
	Groups             int
	LostPackets        int
	RecoveredPackets   int
	UnrecoveredPackets int
	// RecoveryRate = RecoveredPackets / LostPackets (0 when no loss).
	RecoveryRate float64
}

// Decoder holds received media events keyed by sequence and parity groups.
type Decoder struct {
	materialID string
	trackID    string
	groups     []Group
	received   map[uint64]model.Event
	stats      Stats
}

// NewDecoder builds a decoder scoped to one material/track.
func NewDecoder(materialID, trackID string) *Decoder {
	return &Decoder{materialID: materialID, trackID: trackID, received: map[uint64]model.Event{}}
}

// AddGroup registers a parity group before recovery runs.
func (d *Decoder) AddGroup(g Group) error {
	if len(g.Members) < 2 {
		return model.NewError(model.CodeConfig, model.StageRecovery, d.materialID, d.trackID,
			fmt.Sprintf("FEC group %d needs at least two members", g.GroupID))
	}
	d.groups = append(d.groups, g)
	return nil
}

// Observe records a received media event. Duplicate members are ignored here;
// de-duplication is the jitter buffer's responsibility.
func (d *Decoder) Observe(ev model.Event) {
	d.received[ev.Sequence] = ev
}

// Parity computes the XOR parity of the given payloads, matching the fixture
// generator. Short payloads are zero-extended to the longest member.
func Parity(payloads [][]byte) []byte {
	maxLen := 0
	for _, p := range payloads {
		if len(p) > maxLen {
			maxLen = len(p)
		}
	}
	out := make([]byte, maxLen)
	for _, p := range payloads {
		for i, b := range p {
			out[i] ^= b
		}
	}
	return out
}

// Recover processes every group against the declared gaps. It returns:
//   - recovered events (each carrying the gap sequence it fills),
//   - the gaps with Recovered flipped where a repair succeeded,
//   - unrecoverable errors for groups with two or more missing members.
func (d *Decoder) Recover(declaredGaps []model.Gap) ([]model.Event, []model.Gap, []error) {
	gaps := append([]model.Gap(nil), declaredGaps...)
	var recovered []model.Event
	var errs []error

	declared := map[uint64]int{} // seq -> index in gaps
	for i := range gaps {
		for s := gaps[i].FirstSeq; s <= gaps[i].LastSeq; s++ {
			declared[s] = i
		}
	}

	for _, g := range d.groups {
		d.stats.Groups++
		var missing []uint64
		for _, seq := range g.Members {
			if _, ok := d.received[seq]; !ok {
				missing = append(missing, seq)
			}
		}
		if len(missing) == 0 {
			continue
		}
		d.stats.LostPackets += len(missing)
		if len(missing) >= 2 {
			d.stats.UnrecoveredPackets += len(missing)
			errs = append(errs, model.NewError(model.CodeUnrecoverableLoss, model.StageRecovery,
				d.materialID, d.trackID,
				fmt.Sprintf("FEC group %d has %d missing members, single parity cannot repair",
					g.GroupID, len(missing))))
			continue
		}
		missSeq := missing[0]
		gapIdx, isDeclared := declared[missSeq]
		if !isDeclared {
			// Hard invariant: never synthesize a packet for an undeclared hole.
			errs = append(errs, model.NewError(model.CodeInvariant, model.StageRecovery,
				d.materialID, d.trackID,
				fmt.Sprintf("cannot recover seq %d: not part of any declared gap", missSeq)))
			d.stats.UnrecoveredPackets++
			continue
		}
		// missing_payload = parity XOR (all received member payloads)
		restored := append([]byte(nil), g.Parity...)
		for _, seq := range g.Members {
			if seq == missSeq {
				continue
			}
			ev, ok := d.received[seq]
			if !ok {
				continue // impossible: only one member missing in this branch
			}
			restored = xorInto(restored, ev.Payload)
		}
		// Anchor the synthetic event to a real member's timing metadata.
		anchor := d.anchorEvent(g.Members, missSeq)
		anchor.Sequence = missSeq
		anchor.Payload = restored
		anchor.Size = len(restored)
		anchor.Recovered = true
		anchor.HasGapRef = true
		anchor.FillsGapSeq = missSeq
		anchor.Origin = fmt.Sprintf("fec-group-%d-recovered", g.GroupID)
		recovered = append(recovered, anchor)
		d.received[missSeq] = anchor
		d.stats.RecoveredPackets++
		gaps[gapIdx].Recovered = true
	}
	d.stats.RecoveryRate = 0
	if d.stats.LostPackets > 0 {
		d.stats.RecoveryRate = float64(d.stats.RecoveredPackets) / float64(d.stats.LostPackets)
	}
	return recovered, gaps, errs
}

// anchorEvent picks a received member to clone timing fields from.
func (d *Decoder) anchorEvent(members []uint64, missSeq uint64) model.Event {
	for _, seq := range members {
		if ev, ok := d.received[seq]; ok {
			clone := ev
			clone.Payload = nil
			return clone
		}
	}
	// No anchor available: build a minimal event bound to the missing sequence.
	return model.Event{MaterialID: d.materialID, TrackID: d.trackID, Sequence: missSeq}
}

// Stats returns a snapshot of recovery counters.
func (d *Decoder) Stats() Stats { return d.stats }

func xorInto(dst, src []byte) []byte {
	if len(src) > len(dst) {
		grown := make([]byte, len(src))
		copy(grown, dst)
		dst = grown
	}
	for i := 0; i < len(src); i++ {
		dst[i] ^= src[i]
	}
	return dst
}
