// Package rtprec reorders and recovers synthetic RTP packet sequences.
package rtprec

import (
	"encoding/json"
	"os"

	"github.com/pion/rtp"

	"github.com/zld/mediapipe/media"
)

// Packet is one arrival in the synthetic sequence. Raw holds the complete
// RTP packet as pion/rtp marshalled it; the header fields are mirrored here
// so ordering can be done without re-parsing every arrival.
type Packet struct {
	Seq     uint16 `json:"seq"`
	TS      uint32 `json:"ts"`
	Raw     []byte `json:"-"`
	Arrival int    `json:"arrival"`
	Marker  bool   `json:"marker"`
	PT      uint8  `json:"pt"`
	SSRC    uint32 `json:"ssrc"`
}

// FromPion converts a pion/rtp packet into a sequence arrival.
func FromPion(p *rtp.Packet, arrival int) (Packet, error) {
	raw, err := p.Marshal()
	if err != nil {
		return Packet{}, media.WrapError(media.KindInternal, "", err)
	}
	return Packet{
		Seq: p.SequenceNumber, TS: p.Timestamp, Raw: raw, Arrival: arrival,
		Marker: p.Marker, PT: p.PayloadType, SSRC: p.SSRC,
	}, nil
}

// Decode parses the stored wire bytes with pion/rtp.
func (p Packet) Decode() (*rtp.Packet, error) {
	out := &rtp.Packet{}
	if err := out.Unmarshal(p.Raw); err != nil {
		return nil, media.WrapError(media.KindCorruptBox, "", err)
	}
	return out, nil
}

// Sequence is the synthetic input: packets in arrival order plus the ground
// truth of which sequence numbers the sender emitted.
type Sequence struct {
	SSRC      uint32   `json:"ssrc"`
	ClockRate uint32   `json:"clock_rate"`
	Sent      []uint16 `json:"sent_seq"`
	Packets   []Packet `json:"packets"`
}

// DecodePacket parses raw RTP bytes into a packet.
func DecodePacket(raw []byte) (*rtp.Packet, error) {
	p := &rtp.Packet{}
	if err := p.Unmarshal(raw); err != nil {
		return nil, media.WrapError(media.KindCorruptBox, "", err)
	}
	return p, nil
}

// Gap records a contiguous sequence range that could not be recovered.
type Gap struct {
	StartSeq uint16 `json:"start_seq"`
	EndSeq   uint16 `json:"end_seq"`
	Count    int    `json:"count"`
}

// Config controls windowed reassembly.
type Config struct {
	Window      int `json:"window"`       // max packets held out of order
	WaitPackets int `json:"wait_packets"` // arrivals to wait before declaring a loss
}

// DefaultConfig is used when the caller does not specify one.
func DefaultConfig() Config { return Config{Window: 256, WaitPackets: 8} }

func (c Config) validate() error {
	if c.Window < 2 {
		return media.NewError(media.KindArgument, "", "rtp window must be at least 2")
	}
	if c.Window > 65536 {
		return media.NewError(media.KindArgument, "", "rtp window must be at most 65536")
	}
	if c.WaitPackets < 1 {
		return media.NewError(media.KindArgument, "", "rtp wait must be at least 1")
	}
	return nil
}

// Result is the outcome of reassembly.
type Result struct {
	Ordered        []Packet      `json:"-"`
	OrderedCount   int           `json:"ordered_count"`
	SentCount      int           `json:"sent_count"`
	ReceivedCount  int           `json:"received_count"`
	DuplicateCount int           `json:"duplicate_count"`
	RecoveredCount int           `json:"recovered_count"`
	LostCount      int           `json:"lost_count"`
	ReorderedCount int           `json:"reordered_count"`
	Gaps           []Gap         `json:"gaps"`
	Issues         []media.Issue `json:"issues"`
}

// MarshalJSON stores the RTP wire bytes as base64 alongside the mirror fields.
func (p Packet) MarshalJSON() ([]byte, error) {
	type wire struct {
		Seq     uint16 `json:"seq"`
		TS      uint32 `json:"ts"`
		Arrival int    `json:"arrival"`
		Marker  bool   `json:"marker"`
		PT      uint8  `json:"pt"`
		SSRC    uint32 `json:"ssrc"`
		Raw     []byte `json:"raw"`
	}
	return json.Marshal(wire{p.Seq, p.TS, p.Arrival, p.Marker, p.PT, p.SSRC, p.Raw})
}

// UnmarshalJSON restores the packet from its base64 wire bytes.
func (p *Packet) UnmarshalJSON(b []byte) error {
	var w struct {
		Seq     uint16 `json:"seq"`
		TS      uint32 `json:"ts"`
		Arrival int    `json:"arrival"`
		Marker  bool   `json:"marker"`
		PT      uint8  `json:"pt"`
		SSRC    uint32 `json:"ssrc"`
		Raw     []byte `json:"raw"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*p = Packet{Seq: w.Seq, TS: w.TS, Raw: w.Raw, Arrival: w.Arrival,
		Marker: w.Marker, PT: w.PT, SSRC: w.SSRC}
	return nil
}

// RecoveryRate is the fraction of emitted packets present in the output.
func (r *Result) RecoveryRate() float64 {
	if r.SentCount == 0 {
		return 1
	}
	return float64(r.OrderedCount) / float64(r.SentCount)
}

// LoadSequence reads a JSON sequence file.
func LoadSequence(path string) (*Sequence, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, media.WrapError(media.KindIO, path, err)
	}
	var s Sequence
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, media.WrapError(media.KindCorruptBox, path, err)
	}
	if s.ClockRate == 0 {
		s.ClockRate = 90000
	}
	if len(s.Sent) == 0 {
		return nil, media.NewError(media.KindUnsupported, path, "sequence contains no emitted packets")
	}
	return &s, nil
}

// Reassemble orders packets by sequence number, drops duplicates, and waits
// within the configured window for out-of-order arrivals. Sequence runs that
// stay absent after the wait are recorded as gaps and in result issues.
//
// The returned ordering depends only on the arrival list and the config, so
// identical inputs always produce identical output.
func Reassemble(seq *Sequence, cfg Config) (*Result, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	// Indices are positions in the emitted sequence, so 16-bit wraparound is
	// handled without special cases.
	sentIdx := make(map[uint16]int, len(seq.Sent))
	for i, s := range seq.Sent {
		sentIdx[s] = i
	}
	for i := 1; i < len(seq.Sent); i++ {
		if seq.Sent[i] != seq.Sent[0]+uint16(i) {
			return nil, media.NewError(media.KindUnsupported, "", "emitted sequence is not contiguous at index %d", i)
		}
	}

	r := &Result{SentCount: len(seq.Sent), ReceivedCount: len(seq.Packets)}
	buf := make(map[int]Packet, cfg.Window)
	declared := make(map[int]bool, len(seq.Sent))
	emitted := make(map[int]bool, len(seq.Sent))
	expected := 0 // first sequence index not yet emitted
	waited := 0   // arrivals seen since the current hole opened

	commitHole := func() {
		// Walk forward from the frontier: emit buffered packets, and mark as
		// lost every missing index up to the next buffered packet. This
		// commits one contiguous hole at a time.
		if expected >= len(seq.Sent) {
			return
		}
		if p, ok := buf[expected]; ok {
			r.Ordered = append(r.Ordered, p)
			r.OrderedCount++
			emitted[expected] = true
			delete(buf, expected)
			expected++
			return
		}
		for expected < len(seq.Sent) {
			if _, ok := buf[expected]; ok {
				break
			}
			declared[expected] = true
			r.LostCount++
			expected++
		}
	}

	advance := func() {
		for expected < len(seq.Sent) {
			p, ok := buf[expected]
			if !ok {
				return
			}
			r.Ordered = append(r.Ordered, p)
			r.OrderedCount++
			emitted[expected] = true
			delete(buf, expected)
			expected++
			waited = 0
		}
	}

	for _, pkt := range seq.Packets {
		rel, ok := sentIdx[pkt.Seq]
		if !ok {
			r.Issues = append(r.Issues, media.Issue{Kind: media.KindRTPGap, Severity: media.SeverityWarning,
				Detail: "arrival carries a sequence number the sender never emitted",
				At:     pkt.Arrival})
			continue
		}
		if emitted[rel] || declared[rel] {
			// Already delivered or already committed as lost: a duplicate or
			// a too-late arrival. Neither changes the output.
			r.DuplicateCount++
			continue
		}
		if _, dup := buf[rel]; dup {
			r.DuplicateCount++
			continue
		}
		if rel > expected {
			r.ReorderedCount++
		}
		buf[rel] = pkt
		prev := expected
		advance()

		// The wait counts only arrivals that failed to advance the frontier:
		// an arrival that fills or extends the contiguous run resets it.
		if expected == prev {
			waited++
		} else {
			waited = 0
		}
		// A hole that persists for WaitPackets arrivals, or whose backlog
		// exceeds the window, is unrecoverable within this configuration.
		if expected < len(seq.Sent) && (waited >= cfg.WaitPackets || len(buf) > cfg.Window) {
			commitHole()
			waited = 0
		}
	}

	// End of arrivals: every remaining hole is lost, everything buffered is
	// emitted in sequence order.
	for expected < len(seq.Sent) {
		if p, ok := buf[expected]; ok {
			r.Ordered = append(r.Ordered, p)
			r.OrderedCount++
			emitted[expected] = true
			delete(buf, expected)
		} else {
			declared[expected] = true
			r.LostCount++
		}
		expected++
	}
	r.RecoveredCount = r.OrderedCount
	for i := 0; i < len(seq.Sent); {
		if !declared[i] {
			i++
			continue
		}
		j := i
		for j < len(seq.Sent) && declared[j] {
			j++
		}
		r.Gaps = append(r.Gaps, Gap{
			StartSeq: seq.Sent[i],
			EndSeq:   seq.Sent[j-1],
			Count:    j - i,
		})
		i = j
	}
	if r.LostCount > 0 {
		r.Issues = append(r.Issues, media.Issue{Kind: media.KindRTPGap, Severity: media.SeverityError,
			Detail: "unrecoverable RTP sequence gap remains after recovery window",
			At:     int(r.Gaps[0].StartSeq)})
	}
	return r, nil
}
