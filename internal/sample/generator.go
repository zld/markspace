package sample

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"math/rand"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"

	"medpipe/internal/ingest"
)

type Corruption struct {
	RequestedOffset int64  `json:"requested_offset"`
	ActualOffset    int64  `json:"actual_offset"`
	ChunkOffset     int64  `json:"chunk_offset"`
	PacketOffset    int    `json:"packet_offset"`
	PacketSequence  uint16 `json:"packet_sequence"`
	SSRC            uint32 `json:"ssrc"`
}

type Generated struct {
	Config         Config      `json:"config"`
	Data           []byte      `json:"-"`
	Digest         string      `json:"digest"`
	Corruption     *Corruption `json:"corruption,omitempty"`
	CanonicalCount int         `json:"canonical_count"`
	WireChunks     int         `json:"wire_chunks"`
}

type canonicalPacket struct {
	index int
	raw   []byte
	seq   uint16
	ts    uint32
}

// Generate creates a deterministic impaired RTP/RTCP source stream.
func Generate(cfg Config) (*Generated, error) {
	cfg = cfg.withDefaults()
	rng := rand.New(rand.NewSource(cfg.Seed))

	canonical := make([]canonicalPacket, 0, cfg.Packets)
	for i := 0; i < cfg.Packets; i++ {
		patternIndex := i
		if i >= 1000 && i <= 1199 {
			patternIndex = i - 600 // exact replay of frames 400..599
		}
		seq := cfg.StartSequence + uint16(i)
		ts := cfg.StartTimestamp + uint32(i)*cfg.FrameSamples
		key := patternIndex%cfg.KeyFrameEvery == 0
		energy := deterministicEnergy(cfg.Seed, patternIndex)
		if (i >= 700 && i <= 739) || (i >= 5200 && i <= 5239) {
			energy = 20
		}
		if i >= 1600 && i <= 1609 {
			energy = 10
		}

		content := make([]byte, cfg.ContentSize)
		patternID := uint32(patternIndex)
		binary.BigEndian.PutUint32(content[0:4], patternID)
		for j := 4; j < len(content); j++ {
			content[j] = byte(patternID>>uint((j%4)*8)) ^ byte(j*31+int(cfg.Seed))
		}

		payload := ingest.EncodePayload(ingest.FramePayload{
			Flags:           boolFlag(key),
			ContentType:     1,
			FrameIndex:      uint32(i),
			DurationSamples: cfg.FrameSamples,
			Energy:          int32(energy),
			PatternID:       patternID,
			Content:         content,
		})
		pkt := rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				Marker:         key,
				PayloadType:    96,
				SequenceNumber: seq,
				Timestamp:      ts,
				SSRC:           cfg.SSRC,
			},
			Payload: payload,
		}
		raw, err := pkt.Marshal()
		if err != nil {
			return nil, fmt.Errorf("marshal generated RTP packet %d: %w", i, err)
		}
		canonical = append(canonical, canonicalPacket{index: i, raw: raw, seq: seq, ts: ts})
	}

	kept := make([]canonicalPacket, 0, len(canonical))
	for _, p := range canonical {
		if cfg.IsProtected(p.index) || rng.Float64() >= cfg.Loss {
			kept = append(kept, p)
		}
	}
	for i := 0; i+1 < len(kept); i++ {
		if cfg.IsProtected(kept[i].index) || cfg.IsProtected(kept[i+1].index) {
			continue
		}
		if rng.Float64() < cfg.Reorder {
			kept[i], kept[i+1] = kept[i+1], kept[i]
			i++
		}
	}

	chunks := make([]Chunk, 0, len(kept)+len(kept)/100+10)
	baseTime := time.Unix(1_700_000_000, 0).UTC()
	for n, p := range kept {
		chunks = append(chunks, Chunk{Kind: ChunkRTP, Data: p.raw})
		if !cfg.IsProtected(p.index) && rng.Float64() < cfg.Duplicate {
			chunks = append(chunks, Chunk{Kind: ChunkRTP, Data: append([]byte(nil), p.raw...)})
		}
		if !cfg.SuppressRTCP && cfg.RTCPEvery > 0 && (n+1)%cfg.RTCPEvery == 0 {
			mediaNS := uint64(uint32(p.ts-cfg.StartTimestamp)) * uint64(time.Second) / uint64(cfg.SampleRate)
			sr := &rtcp.SenderReport{
				SSRC: cfg.SSRC, NTPTime: ingest.TimeToNTP(baseTime.Add(time.Duration(mediaNS))),
				RTPTime: p.ts, PacketCount: uint32(n + 1), OctetCount: uint32((n + 1) * len(p.raw)),
			}
			raw, err := sr.Marshal()
			if err != nil {
				return nil, fmt.Errorf("marshal RTCP report: %w", err)
			}
			chunks = append(chunks, Chunk{Kind: ChunkRTCP, Data: raw})
		}
	}

	data := WriteContainer(chunks)
	var corruption *Corruption
	if cfg.CorruptOffset > 0 {
		if cfg.CorruptOffset >= int64(len(data)) {
			return nil, fmt.Errorf("corrupt offset %d beyond source size %d", cfg.CorruptOffset, len(data))
		}
		off := cfg.CorruptOffset
		var target *Chunk
		for i := range chunks {
			end := chunks[i].Offset + int64(8+len(chunks[i].Data))
			if off >= chunks[i].Offset && off < end {
				target = &chunks[i]
				break
			}
		}
		if target == nil {
			return nil, fmt.Errorf("could not map corruption offset %d to chunk", off)
		}
		corruption = &Corruption{
			RequestedOffset: off, ActualOffset: off, ChunkOffset: target.Offset,
			PacketOffset: int(off - (target.Offset + 8)), SSRC: cfg.SSRC,
		}
		if target.Kind == ChunkRTP && len(target.Data) >= 4 {
			corruption.PacketSequence = binary.BigEndian.Uint16(target.Data[2:4])
		}
		data[off] ^= 0xff
	}

	sum := sha256.Sum256(data)
	return &Generated{
		Config: cfg, Data: data, Digest: hex.EncodeToString(sum[:]), Corruption: corruption,
		CanonicalCount: len(canonical), WireChunks: len(chunks),
	}, nil
}

func boolFlag(b bool) byte {
	if b {
		return 1
	}
	return 0
}

func deterministicEnergy(seed int64, index int) int {
	h := fnv.New64a()
	var buf [16]byte
	binary.LittleEndian.PutUint64(buf[0:8], uint64(seed))
	binary.LittleEndian.PutUint64(buf[8:16], uint64(index))
	_, _ = h.Write(buf[:])
	return 1500 + int(h.Sum64()%7000)
}
