// Package rtpx applies one rational mapping to RTP timestamps and verifies packet order.
package rtpx

import (
	"fmt"

	"github.com/pion/rtp"

	"mediacore/internal/clock"
	"mediacore/internal/diagnostic"
)

const maxUint32 = ^uint32(0)

// TimestampMapper extends 32-bit RTP timestamps and maps them to unified ticks.
type TimestampMapper struct {
	ClockRate  int64
	ReferenceU int64
	cycles     uint64
	last       *uint32
}

func NewTimestampMapper(clockRate int64, referenceU int64) (*TimestampMapper, error) {
	if clockRate <= 0 {
		return nil, diagnostic.New(diagnostic.ErrZeroDenominator,
			diagnostic.Location{Stage: "rtp", ByteOffset: -1}, "RTP clock rate must be positive")
	}
	return &TimestampMapper{ClockRate: clockRate, ReferenceU: referenceU}, nil
}

// Unwrap returns the monotonically extended timestamp. It assumes in-order RTP packets.
func (m *TimestampMapper) Unwrap(ts uint32) uint64 {
	if m.last != nil {
		last := *m.last
		if ts < last && uint64(last)-uint64(ts) > uint64(maxUint32)/2 {
			m.cycles++
		}
	}
	v := m.cycles*(uint64(maxUint32)+1) + uint64(ts)
	m.last = &ts
	return v
}

// Map extends ts and converts it to the project unified time base.
func (m *TimestampMapper) Map(ts uint32) (extended uint64, unifiedU int64, err error) {
	extended = m.Unwrap(ts)
	rel, err := clock.ToUnified(int64(extended), m.ClockRate)
	if err != nil {
		return extended, 0, err
	}
	unifiedU, err = clock.Add(m.ReferenceU, rel)
	return extended, unifiedU, err
}

// PacketOrder describes sequence verification results.
type PacketOrder struct {
	PacketCount  int      `json:"packet_count"`
	Sequences    []uint16 `json:"sequences"`
	Duplicates   []uint16 `json:"duplicates,omitempty"`
	OutOfOrder   []uint16 `json:"out_of_order,omitempty"`
	MissingCount int      `json:"missing_count"`
	Ordered      bool     `json:"ordered"`
}

// VerifyPackets unmarshals raw RTP packets with pion/rtp and checks wrap-aware order.
func VerifyPackets(raw [][]byte) (PacketOrder, error) {
	report := PacketOrder{Ordered: true, Sequences: make([]uint16, 0, len(raw))}
	seen := map[uint16]bool{}
	var last uint16
	have := false
	for i, b := range raw {
		var p rtp.Packet
		if err := p.Unmarshal(b); err != nil {
			return report, diagnostic.Wrap(nil, diagnostic.Location{
				Stage: "rtp", EntryIndex: i, ByteOffset: 0, Field: "packet",
			}, fmt.Errorf("rtp packet %d: %w", i, err))
		}
		seq := p.SequenceNumber
		report.Sequences = append(report.Sequences, seq)
		if seen[seq] {
			report.Duplicates = append(report.Duplicates, seq)
			continue
		}
		seen[seq] = true
		if have {
			delta := forwardDelta16(last, seq)
			if delta == 0 {
				report.Duplicates = append(report.Duplicates, seq)
			} else {
				report.MissingCount += int(delta - 1)
				// A small backward distance means reordering; a near-full forward
				// distance is a wrap and is still ordered.
				backward := uint32(last) - uint32(seq)
				if seq < last && backward < 32768 {
					report.OutOfOrder = append(report.OutOfOrder, seq)
					report.Ordered = false
				}
			}
		}
		last = seq
		have = true
	}
	return report, nil
}

func forwardDelta16(from, to uint16) uint16 {
	return uint16(uint32(to) - uint32(from))
}
