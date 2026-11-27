package fixture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"

	pionrtp "github.com/pion/rtp"
)

// EventRecord is one JSONL line: a timestamped RTP delivery observation.
// The RTP packet is stored as a hex-encoded wire packet so the pipeline can
// exercise the real github.com/pion/rtp unmarshal path.
type EventRecord struct {
	ArrivalNs int64  `json:"arrival_ns"`
	SSRC      uint32 `json:"ssrc"`
	Sequence  uint16 `json:"sequence"`
	RTPTime   uint32 `json:"rtp_timestamp"`
	Marker    bool   `json:"marker"`
	RawHex    string `json:"rtp_hex"`
}

// EventConfig controls RTP event-record generation.
type EventConfig struct {
	Seed       int64
	Count      int
	SSRC       uint32
	StartSeq   uint16
	ClockRate  uint32
	GapEvery   int   // create a gap every N sequence steps
	LateWindow int64 // recovery window in nanoseconds
	Recovered  int   // gaps healed by late arrivals inside the window
}

// DefaultEventConfig returns the acceptance configuration: five gaps, four
// healed inside the recovery window (0.8 recovery rate) and one permanent
// loss whose late packet arrives outside the window, plus an exact duplicate.
func DefaultEventConfig(seed int64) EventConfig {
	return EventConfig{
		Seed:       seed,
		Count:      40,
		SSRC:       0xC0FFEE,
		StartSeq:   1000,
		ClockRate:  90000,
		GapEvery:   7,
		LateWindow: 5_000_000, // 5 ms
		Recovered:  4,
	}
}

type rtpEntry struct {
	seq    uint16
	ns     int64
	dup    bool
	marker bool
}

// GenerateEvents renders deterministic RTP event records in arrival order.
// The second return value is the generator's expected statistics, matching
// the classification rules in internal/rtp exactly.
func GenerateEvents(cfg EventConfig) ([]EventRecord, RTPTruth) {
	rng := rand.New(rand.NewSource(cfg.Seed))
	baseNs := func(seq uint16) int64 {
		return int64(seq-cfg.StartSeq) * 1_000_000
	}

	var entries []rtpEntry
	var gapSeqs []uint16
	for i := 0; i < cfg.Count; i++ {
		seq := cfg.StartSeq + uint16(i)
		isGap := i > 0 && i%cfg.GapEvery == 0
		if isGap {
			gapSeqs = append(gapSeqs, seq)
			continue // gap: no in-order arrival
		}
		entries = append(entries, rtpEntry{seq: seq, ns: baseNs(seq), marker: i == cfg.Count-1})
	}
	truth := RTPTruth{RecoveryWindowNs: cfg.LateWindow, Gaps: len(gapSeqs)}

	// First Recovered gaps: a late packet inside the window closes the gap
	// (recovered + reordered). The final gap instead heals only AFTER the
	// window expired: the loss is already committed, the late arrival is a
	// pure reorder and must not rewrite the loss statistic.
	for i, seq := range gapSeqs {
		switch {
		case i < cfg.Recovered:
			entries = append(entries, rtpEntry{
				seq: seq,
				ns:  baseNs(seq) + cfg.LateWindow/2, // strictly inside window
			})
		default:
			entries = append(entries, rtpEntry{
				seq: seq,
				ns:  baseNs(seq) + cfg.LateWindow*2, // outside window
			})
		}
	}
	truth.Recovered = cfg.Recovered
	truth.PermanentLoss = len(gapSeqs) - cfg.Recovered

	// One exact duplicate of an early, already-delivered packet.
	dupSeq := cfg.StartSeq + 2
	entries = append(entries, rtpEntry{seq: dupSeq, ns: baseNs(dupSeq) + 200_000, dup: true})
	truth.Duplicates = 1
	// Reordered counts every gap fill (in-window) plus the one out-of-window
	// late arrival, which remains a reorder without touching committed loss.
	truth.Reordered = cfg.Recovered + 1

	sort.SliceStable(entries, func(i, j int) bool { return entries[i].ns < entries[j].ns })

	var out []EventRecord
	rtpTime := uint32(0)
	for _, e := range entries {
		pkt := &pionrtp.Packet{
			Header: pionrtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: e.seq,
				Timestamp:      rtpTime,
				SSRC:           cfg.SSRC,
				Marker:         e.marker,
			},
			Payload: []byte{byte(rng.Intn(256)), byte(rng.Intn(256)), 0x10},
		}
		rtpTime += cfg.ClockRate / 1000
		raw, err := pkt.Marshal()
		if err != nil {
			panic(err)
		}
		out = append(out, EventRecord{
			ArrivalNs: e.ns,
			SSRC:      cfg.SSRC,
			Sequence:  e.seq,
			RTPTime:   pkt.Timestamp,
			Marker:    e.marker,
			RawHex:    fmt.Sprintf("%x", raw),
		})
	}
	return out, truth
}

// RTPTruth is the generator's expected statistics for cross-validation.
type RTPTruth struct {
	RecoveryWindowNs int64
	Gaps             int
	Recovered        int
	PermanentLoss    int
	Duplicates       int
	Reordered        int
}

// MarshalEvents writes records as newline-delimited JSON.
func MarshalEvents(recs []EventRecord) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	for _, r := range recs {
		if err := enc.Encode(r); err != nil {
			panic(err)
		}
	}
	return b.Bytes()
}

// CycleTracks returns track descriptors forming an explicit dependency cycle,
// plus one independent track that must still process normally.
func CycleTracks() []CycleTrackSpec {
	return []CycleTrackSpec{
		{ID: 1, Codec: "V_VP9", Kind: 1, DependsOn: []uint64{2}},
		{ID: 2, Codec: "V_VP9", Kind: 1, DependsOn: []uint64{3}},
		{ID: 3, Codec: "V_VP9", Kind: 1, DependsOn: []uint64{1}},
		{ID: 9, Codec: "A_OPUS", Kind: 2, DependsOn: nil},
	}
}

// CycleTrackSpec is one track entry of the dependency-cycle fixture.
type CycleTrackSpec struct {
	ID        uint64   `json:"id"`
	Codec     string   `json:"codec"`
	Kind      uint8    `json:"kind"`
	DependsOn []uint64 `json:"depends_on"`
}
