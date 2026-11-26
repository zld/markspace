package fixture

import (
	"path/filepath"

	"github.com/pion/rtp"

	rtprec "continuity/internal/rtp"
)

// RTPFixtureSummary records the generated loss geometry.
type RTPFixtureSummary struct {
	Path          string
	Recovered     int
	Lost          int
	Unrecoverable int
}

func buildCleanRTP(root string) (RTPFixtureSummary, error) {
	path := filepath.Join(root, "rtp", "records.rtp")
	const ssrc = 0x17579
	packets := make([]*rtp.Packet, 0)
	for g := 0; g < 4; g++ {
		base := uint16(100 + g*3)
		seqs := []uint16{base, base + 1, base + 2}
		payloads := [][]byte{
			patternBytes(16, byte(base)),
			patternBytes(16, byte(base+1)),
			patternBytes(16, byte(base+2)),
		}
		all := []*rtp.Packet{
			rtprec.NewMediaPacket(seqs[0], uint32(seqs[0])*3000, payloads[0]),
			rtprec.NewMediaPacket(seqs[1], uint32(seqs[1])*3000, payloads[1]),
			rtprec.NewMediaPacket(seqs[2], uint32(seqs[2])*3000, payloads[2]),
		}
		fec, err := rtprec.BuildFECPacket(ssrc, uint16(200+g), byte(g+1), seqs, payloads)
		if err != nil {
			return RTPFixtureSummary{}, err
		}
		// The middle packet is absent. Arrival is reversed and includes one
		// duplicate of the first available packet in each group.
		packets = append(packets, all[2], all[0], all[0], fec)
	}
	if err := rtprec.WriteRecords(path, packets); err != nil {
		return RTPFixtureSummary{}, err
	}
	return RTPFixtureSummary{Path: path, Recovered: 4, Lost: 4, Unrecoverable: 0}, nil
}

func buildBoundaryRTP(root string) (RTPFixtureSummary, error) {
	path := filepath.Join(root, "rtp", "unrecoverable.rtp")
	seqs := []uint16{30, 31, 32}
	payloads := [][]byte{[]byte("boundary-a-000000"), []byte("boundary-b-000000"), []byte("boundary-c-000000")}
	all := []*rtp.Packet{
		rtprec.NewMediaPacket(30, 90000, payloads[0]),
		rtprec.NewMediaPacket(31, 180000, payloads[1]),
		rtprec.NewMediaPacket(32, 270000, payloads[2]),
	}
	fec, err := rtprec.BuildFECPacket(0x42, 230, 9, seqs, payloads)
	if err != nil {
		return RTPFixtureSummary{}, err
	}
	// Two of three media packets never arrive; one XOR block cannot recover both.
	if err := rtprec.WriteRecords(path, []*rtp.Packet{all[0], fec}); err != nil {
		return RTPFixtureSummary{}, err
	}
	return RTPFixtureSummary{Path: path, Recovered: 0, Lost: 2, Unrecoverable: 2}, nil
}
