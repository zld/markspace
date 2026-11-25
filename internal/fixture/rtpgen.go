package fixture

import (
	"fmt"
	"math/rand"

	pionrtp "github.com/pion/rtp"
)

// GenerateRTP creates a seeded weak-network arrival trajectory. The same seed
// always produces the same dropped, duplicated, reordered and late packets.
func GenerateRTP(seed int64, n int) ([][]byte, error) {
	if n <= 1 {
		n = 2
	}
	rng := rand.New(rand.NewSource(seed))
	ssrc := uint32(0x51000000 ^ seed)
	start := uint16(65520) // deliberately close to the 16-bit wraparound

	source := make([]pionrtp.Packet, n)
	for i := 0; i < n; i++ {
		source[i] = pionrtp.Packet{
			Header: pionrtp.Header{
				Version: 2, PayloadType: 96, SSRC: ssrc,
				SequenceNumber: start + uint16(i),
				Timestamp:      uint32(i * 90000),
				Marker:         i%12 == 11,
			},
			Payload: []byte(fmt.Sprintf("seed-%d-sample-%04d", seed, i+1)),
		}
	}

	perm := rng.Perm(n)
	drop := make(map[int]bool, 8)
	for _, idx := range perm[:8] {
		drop[idx] = true
	}
	duplicate := make(map[int]bool, 6)
	for _, idx := range perm[8:14] {
		if !drop[idx] {
			duplicate[idx] = true
		}
	}
	lateIndex := 18
	if drop[lateIndex] {
		lateIndex = 19
		drop[lateIndex] = false
	}
	drop[lateIndex] = false

	swapStart := map[int]bool{24: true, 48: true, 72: true, 96: true}
	arrivals := make([]pionrtp.Packet, 0, n+len(duplicate))
	for i := 0; i < n; {
		if drop[i] || i == lateIndex {
			i++
			continue
		}
		if swapStart[i] && i+1 < n && !drop[i+1] && i+1 != lateIndex {
			arrivals = append(arrivals, source[i+1], source[i])
			if duplicate[i] {
				arrivals = append(arrivals, source[i])
			}
			i += 2
			continue
		}
		arrivals = append(arrivals, source[i])
		if duplicate[i] {
			arrivals = append(arrivals, source[i])
		}
		i++
	}
	// A very late arrival: outside the analyzer's 64-sequence window.
	arrivals = append(arrivals, source[lateIndex])

	blobs := make([][]byte, 0, len(arrivals))
	for i, p := range arrivals {
		b, err := p.Marshal()
		if err != nil {
			return nil, fmt.Errorf("marshal generated packet %d: %w", i, err)
		}
		blobs = append(blobs, b)
	}
	return blobs, nil
}
