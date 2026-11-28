package media

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"sort"

	"github.com/pion/rtp"
)

// RTPSpec describes a synthetic packet stream for one track.
type RTPSpec struct {
	Asset     string
	Track     uint32
	ClockRate uint32
	StartRTP  uint32
	StartSeq  uint16
	Packets   int
	Payload   int
	// TickEvery is how many packets advance one RTP timestamp tick.
	TickEvery int
	// ReorderAhead packets are sent early and then repeated: these are the
	// losses the window is expected to repair.
	ReorderAhead int
	// UnrecoverableGaps is how many runs of missing packets are far larger
	// than the configured window.
	UnrecoverableGaps int
	UnrecoverableRun  int
	// ReorderableGaps is how many packets are dropped for a short while and
	// then re-sent after their neighbours, i.e. true recoverable loss.
	ReorderableGaps int
	ReorderDelay    int
	Duplicates      int
	// JitterPPM adds a deterministic, bounded arrival delay per packet so
	// latency has a realistic distribution. It is integer-only.
	JitterPPM int
}

// RTPPacket is a synthetic packet with the metadata the window needs.
type RTPPacket struct {
	Sequence  uint16
	Timestamp uint32
	Marker    bool
	Payload   []byte
	Raw       []byte // marshalled wire packet
	ArrivalUS int64  // synthetic arrival delay
	Track     uint32
	PayloadID uint16
	Source    int // 0 = normal, 1 = re-ordered, 2 = duplicate, 3 = late repair
}

// MarshalRTP serialises the packet with pion/rtp.
func (p RTPPacket) MarshalRTP() ([]byte, error) {
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			Marker:         p.Marker,
			PayloadType:    96,
			SequenceNumber: p.Sequence,
			Timestamp:      p.Timestamp,
			SSRC:           p.Track,
		},
		Payload: p.Payload,
	}
	return pkt.Marshal()
}

// UnmarshalRTP parses a wire packet with pion/rtp.
func UnmarshalRTP(raw []byte) (RTPPacket, error) {
	var pkt rtp.Packet
	if err := pkt.Unmarshal(raw); err != nil {
		return RTPPacket{}, err
	}
	return RTPPacket{
		Sequence:  pkt.SequenceNumber,
		Timestamp: pkt.Timestamp,
		Marker:    pkt.Marker,
		Payload:   append([]byte(nil), pkt.Payload...),
		Raw:       raw,
		Track:     pkt.SSRC,
	}, nil
}

// RTPStream is a generated stream plus its ground truth.
type RTPStream struct {
	Packets []RTPPacket
	// SentUnique is the number of distinct sequence numbers the sender
	// intended, and Missing lists those deliberately not transmitted.
	SentUnique int
	Missing    []uint16
	Duplicates int
	// ExpectedLossRecoverable counts the missing packets that lie inside the
	// window relative to the first packet after the gap, i.e. the packets a
	// correct window must recover.
	ExpectedLossRecoverable int
	ExpectedLossTotal       int
}

// DecodePackets parses every marshalled packet back through pion/rtp and
// checks the wire representation survived the round trip.
func (s RTPStream) DecodePackets() ([]RTPPacket, error) {
	out := make([]RTPPacket, 0, len(s.Packets))
	for _, p := range s.Packets {
		got, err := UnmarshalRTP(p.Raw)
		if err != nil {
			return nil, fmt.Errorf("media: unmarshal rtp packet %d: %w", p.Sequence, err)
		}
		if got.Sequence != p.Sequence || got.Timestamp != p.Timestamp || got.Marker != p.Marker {
			return nil, fmt.Errorf("media: rtp round trip mismatch at %d", p.Sequence)
		}
		out = append(out, got)
	}
	return out, nil
}

// ArrivalDelays returns the synthetic per-packet network arrival delay. The
// reorder window's added queueing delay is modelled separately by the caller
// from the actual positions packets were emitted at.
func (s RTPStream) ArrivalDelays() []int64 {
	out := make([]int64, len(s.Packets))
	for i, p := range s.Packets {
		out[i] = p.ArrivalUS
	}
	return out
}

// BuildRTP generates a packet stream in arrival order. Packets are keyed so
// two runs with the same spec produce identical bytes.
func BuildRTP(spec RTPSpec) (RTPStream, error) {
	if spec.Packets <= 0 {
		return RTPStream{}, fmt.Errorf("media: rtp packet count %d", spec.Packets)
	}
	if spec.ClockRate == 0 || spec.Payload <= 0 || spec.TickEvery <= 0 {
		return RTPStream{}, fmt.Errorf("media: invalid rtp spec %+v", spec)
	}
	stream := RTPStream{SentUnique: spec.Packets}
	drop := map[int]bool{}
	// Unrecoverable loss runs are placed in the middle of the stream.
	for g := 0; g < spec.UnrecoverableGaps; g++ {
		start := spec.Packets/2 + g*spec.UnrecoverableRun
		for i := 0; i < spec.UnrecoverableRun; i++ {
			idx := start + i
			if idx < spec.Packets {
				drop[idx] = true
			}
		}
	}
	// Re-orderable late arrivals: the packet is sent after ReorderDelay of
	// its successors but still well inside the window, so a correct window
	// recovers it.
	reorder := map[int]int{} // index -> arrival offset past its neighbours
	if spec.ReorderAhead > 0 {
		delay := spec.ReorderDelay
		if delay <= 0 {
			delay = 2
		}
		for i := spec.ReorderAhead; i < spec.Packets-spec.ReorderAhead; i += spec.ReorderAhead {
			reorder[i] = delay
		}
	}
	// Recoverable losses: the packet is not sent with its neighbours but is
	// re-sent ReorderDelay slots later, inside the window.
	late := map[int]bool{}
	for g := 0; g < spec.ReorderableGaps; g++ {
		idx := spec.Packets/3 + g*7
		if idx < spec.Packets && !drop[idx] {
			late[idx] = true
		}
	}

	type entry struct {
		pkt  RTPPacket
		idx  int
		kind int
		late bool
	}
	var entries []entry
	for i := 0; i < spec.Packets; i++ {
		if drop[i] {
			stream.Missing = append(stream.Missing, spec.StartSeq+uint16(i))
			continue
		}
		p := RTPPacket{
			Sequence:  spec.StartSeq + uint16(i),
			Timestamp: spec.StartRTP + uint32(i/spec.TickEvery),
			Marker:    i == spec.Packets-1,
			Payload:   samplePayload(spec, i),
			Track:     spec.Track,
			PayloadID: uint16(i),
		}
		if spec.JitterPPM > 0 {
			p.ArrivalUS = jitter(i, spec.JitterPPM)
		}
		if d, ok := reorder[i]; ok {
			// Arrives after the following packets but inside the window.
			entries = append(entries, entry{pkt: p, idx: i + d, kind: 1})
			continue
		}
		if late[i] {
			entries = append(entries, entry{pkt: p, idx: i + 3, kind: 3})
			continue
		}
		entries = append(entries, entry{pkt: p, idx: i, kind: 0})
	}
	// Duplicates re-send a packet once, after it was already committed.
	for d := 0; d < spec.Duplicates && d < len(entries); d++ {
		src := entries[d*3%len(entries)]
		dup := src.pkt
		dup.Source = 2
		entries = append(entries, entry{pkt: dup, idx: src.idx + 3, kind: 2})
		stream.Duplicates++
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].idx != entries[j].idx {
			return entries[i].idx < entries[j].idx
		}
		return entries[i].pkt.Sequence < entries[j].pkt.Sequence
	})
	for _, e := range entries {
		p := e.pkt
		switch e.kind {
		case 1:
			p.Source = 1
		case 2:
			p.Source = 2
		case 3:
			p.Source = 3
		}
		raw, err := p.MarshalRTP()
		if err != nil {
			return RTPStream{}, fmt.Errorf("media: marshal rtp packet %d: %w", p.Sequence, err)
		}
		p.Raw = raw
		stream.Packets = append(stream.Packets, p)
	}
	// Recoverable loss counts both the late re-sends and the deliberately
	// out-of-order arrivals; unrecoverable loss is the dropped runs.
	stream.ExpectedLossRecoverable = len(reorder) + len(late)
	stream.ExpectedLossTotal = stream.ExpectedLossRecoverable + len(stream.Missing)
	return stream, nil
}

func samplePayload(spec RTPSpec, index int) []byte {
	out := make([]byte, spec.Payload)
	h := fnv.New64a()
	fmt.Fprintf(h, "%s/%d/%d", spec.Asset, spec.Track, index)
	seed := h.Sum64()
	binary.BigEndian.PutUint64(out[:8], seed)
	for i := 8; i < len(out); i++ {
		seed ^= seed << 13
		seed ^= seed >> 7
		seed ^= seed << 17
		out[i] = byte(seed)
	}
	return out
}

// jitter returns a deterministic delay in microseconds bounded by ratePPM of a
// 20 ms reference frame, spread by a cheap integer hash.
func jitter(i, ratePPM int) int64 {
	maxUS := int64(20_000) * int64(ratePPM) / 1_000_000
	if maxUS <= 0 {
		return 0
	}
	h := uint64(i)*2654435761 + 0x9e3779b97f4a7c15
	h ^= h >> 33
	h *= 0xff51afd7ed558ccd
	h ^= h >> 33
	return int64(h % uint64(maxUS))
}
