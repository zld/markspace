package jitter

import (
	"time"

	"jitterlab/internal/packet"
)

// Store is one sequence-ordered buffered packet.
type Store struct {
	Seq        uint32
	Timestamp  uint32
	Arrival    time.Duration
	Frames     byte
	EndOfFrame bool
	Payload    []byte
}

type streamQueue struct {
	playSeq   uint32
	started   bool
	buf       map[uint32]Store
	lastStamp uint32

	// played remembers recently played sequences (bounded to twice the queue
	// capacity) so a duplicate of an already-played packet is still
	// classified as a duplicate rather than as a late packet.
	played      map[uint32]struct{}
	playedOrder []uint32
}

func newStore(p *packet.Packet, arrival time.Duration) Store {
	return Store{
		Seq:        p.Header.Seq,
		Timestamp:  p.Header.Timestamp,
		Arrival:    arrival,
		Frames:     p.Header.Frames,
		EndOfFrame: p.EndOfFrame(),
		Payload:    append([]byte(nil), p.Payload...),
	}
}
