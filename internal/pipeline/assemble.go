package pipeline

import (
	"errors"
	"fmt"

	"mediapipe/internal/rtp"
)

// Reassembly limits.
const (
	MaxGroupsPerSegment  = 512
	MaxFragmentsPerGroup = 64
)

var (
	// ErrFragmentOutOfRange reports a fragment index beyond its group count.
	ErrFragmentOutOfRange = errors.New("pipeline: fragment index out of range")
	// ErrFragmentConflict reports two different payloads for one fragment.
	ErrFragmentConflict = errors.New("pipeline: conflicting fragment payload")
)

// reassembly reconstructs groups from emitted fragments and keeps an explicit
// missing list for groups the network never completed.
type reassembly struct {
	groups   map[uint32]map[uint32]*partialGroup // track -> frame -> group
	order    []groupKey
	missing  []groupKey
	complete map[groupKey]bool
}

type groupKey struct {
	track uint32
	frame uint32
}

type partialGroup struct {
	segment uint32
	flags   uint8
	parts   [][]byte
	have    []bool
	count   uint16
}

func newReassembly() *reassembly {
	return &reassembly{groups: map[uint32]map[uint32]*partialGroup{}, complete: map[groupKey]bool{}}
}

func (a *reassembly) add(p rtp.Packet) error {
	pl, err := rtp.ParsePayload(p.Payload)
	if err != nil {
		return err
	}
	if pl.FragCount == 0 || pl.FragIndex >= pl.FragCount {
		return fmt.Errorf("%w: %d/%d", ErrFragmentOutOfRange, pl.FragIndex, pl.FragCount)
	}
	key := groupKey{uint32(pl.TrackID), pl.Frame}
	if a.complete[key] {
		return nil
	}
	byFrame, ok := a.groups[uint32(pl.TrackID)]
	if !ok {
		byFrame = map[uint32]*partialGroup{}
		a.groups[uint32(pl.TrackID)] = byFrame
	}
	g, ok := byFrame[pl.Frame]
	if !ok {
		if len(a.order) >= MaxGroupsPerSegment*32 {
			return fmt.Errorf("pipeline: too many groups")
		}
		g = &partialGroup{parts: make([][]byte, pl.FragCount), have: make([]bool, pl.FragCount), count: pl.FragCount, flags: pl.Flags}
		byFrame[pl.Frame] = g
		a.order = append(a.order, key)
	}
	if g.count != pl.FragCount {
		return fmt.Errorf("%w: group %d count %d vs %d", ErrFragmentConflict, pl.Frame, pl.FragCount, g.count)
	}
	if g.have[pl.FragIndex] {
		if string(g.parts[pl.FragIndex]) != string(pl.Data) {
			return fmt.Errorf("%w: group %d fragment %d", ErrFragmentConflict, pl.Frame, pl.FragIndex)
		}
		return nil
	}
	g.parts[pl.FragIndex] = append([]byte(nil), pl.Data...)
	g.have[pl.FragIndex] = true
	if g.flags != pl.Flags {
		return fmt.Errorf("%w: group %d flags", ErrFragmentConflict, pl.Frame)
	}
	return nil
}

// completeGroups returns every group that has all fragments.
func (a *reassembly) completeGroups() []groupKey {
	var out []groupKey
	for _, k := range a.order {
		if a.complete[k] {
			continue
		}
		g := a.groups[k.track][k.frame]
		if g == nil || len(g.parts) != int(g.count) {
			continue
		}
		all := true
		for _, have := range g.have {
			if !have {
				all = false
				break
			}
		}
		if all {
			a.complete[k] = true
			out = append(out, k)
		}
	}
	return out
}

// data reassembles the complete group's payload.
func (a *reassembly) data(k groupKey) []byte {
	g := a.groups[k.track][k.frame]
	var out []byte
	for _, p := range g.parts {
		out = append(out, p...)
	}
	return out
}

// incomplete lists groups with at least one missing fragment.
func (a *reassembly) incomplete() []groupKey {
	var out []groupKey
	for _, k := range a.order {
		if a.complete[k] {
			continue
		}
		g := a.groups[k.track][k.frame]
		if g == nil {
			continue
		}
		for _, have := range g.have {
			if !have {
				out = append(out, k)
				break
			}
		}
	}
	return out
}
