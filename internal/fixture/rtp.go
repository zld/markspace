package fixture

import (
	"encoding/binary"
	"os"

	"github.com/pion/rtp"
)

// RTPEdit scripts arrival-order anomalies for a generated RTP dump.
type RTPEdit struct {
	// Late lists packet indices retransmitted LateBy arrivals after
	// their original position (recoverable loss).
	Late   []int
	LateBy int
	// Drop lists packet indices that never arrive (unrecoverable loss).
	Drop []int
	// Dup lists packet indices transmitted twice.
	Dup []int
	// Swap lists adjacent index pairs exchanged in arrival order.
	Swap [][2]int
}

// WriteRTP writes a deterministic RTP dump of [u16 BE len][packet]
// records. seqStart and tsStart may sit near the modulus boundary to
// exercise wrap-around; deltas are per-packet timestamp increments.
func WriteRTP(path string, seqStart uint16, tsStart uint32, ssrc uint32,
	deltas []uint32, edit RTPEdit) error {
	n := len(deltas)
	pkts := make([][]byte, n)
	var ts uint32 = tsStart
	for i := 0; i < n; i++ {
		payload := make([]byte, 32)
		for j := range payload {
			payload[j] = byte(i*13 + j*5)
		}
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				SequenceNumber: seqStart + uint16(i),
				Timestamp:      ts,
				SSRC:           ssrc,
			},
			Payload: payload,
		}
		raw, err := pkt.Marshal()
		if err != nil {
			return err
		}
		pkts[i] = raw
		ts += deltas[i]
	}

	// Arrival order: base sequence with scripted anomalies applied.
	dropped := map[int]bool{}
	for _, d := range edit.Drop {
		dropped[d] = true
	}
	late := map[int]bool{}
	for _, l := range edit.Late {
		late[l] = true
	}
	swapped := map[int]int{}
	for _, sw := range edit.Swap {
		swapped[sw[0]] = sw[1]
		swapped[sw[1]] = sw[0]
	}
	var order []int
	for i := 0; i < n; i++ {
		if dropped[i] || late[i] {
			continue
		}
		j := i
		if s, ok := swapped[i]; ok {
			j = s
		}
		order = append(order, j)
	}
	// Re-insert late packets LateBy arrivals after their original spot.
	if edit.LateBy <= 0 {
		edit.LateBy = 3
	}
	for _, l := range edit.Late {
		pos := l + edit.LateBy
		if pos > len(order) {
			pos = len(order)
		}
		order = append(order[:pos], append([]int{l}, order[pos:]...)...)
	}
	// Duplicates ride right after their original arrival.
	var final []int
	for _, idx := range order {
		final = append(final, idx)
		dup := false
		for _, d := range edit.Dup {
			if d == idx {
				dup = true
			}
		}
		if dup {
			final = append(final, idx)
		}
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var hdr [2]byte
	for _, idx := range final {
		binary.BigEndian.PutUint16(hdr[:], uint16(len(pkts[idx])))
		if _, err := f.Write(hdr[:]); err != nil {
			return err
		}
		if _, err := f.Write(pkts[idx]); err != nil {
			return err
		}
	}
	return nil
}
