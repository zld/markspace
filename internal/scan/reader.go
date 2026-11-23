// Package scan reads frame sequences, computes input digests and builds
// digest-bound scan indexes over the captured stream.
package scan

import (
	"encoding/binary"
	"hash/crc32"
	"io"
	"math/rand"

	"github.com/pion/rtp"

	"mediainspect/internal/media"
)

// StreamID is the fixed stream identifier used for locally generated samples.
const StreamID uint32 = 0x15578

// FrameTicks is the nominal frame duration in timebase ticks (30 fps @ 90 kHz).
const FrameTicks = 3000

// GenConfig configures deterministic sample generation and impairments.
type GenConfig struct {
	Seed             int64
	Frames           int
	TargetKbps       float64
	KeyframeInterval int // frames between keyframes; default 30
	LossBurst        int // drop N consecutive frames mid-stream
	ReorderWindow    int // shuffle frames within windows of this size
	Duplicates       int // number of frames emitted twice
	CorruptChecksum  bool
	TruncateFrame    bool
	BadAudioLayout   bool
	ColorConflict    bool
	Wrap             bool // start seq/timestamp near wrap boundaries
}

func (c GenConfig) withDefaults() GenConfig {
	if c.Frames <= 0 {
		c.Frames = 3000
	}
	if c.KeyframeInterval <= 0 {
		c.KeyframeInterval = 30
	}
	if c.TargetKbps <= 0 {
		c.TargetKbps = 1200
	}
	return c
}

type frameSpec struct {
	seq                     uint16
	ts                      uint32
	keyframe                bool
	size, declared          uint32
	checksum, payloadCRC    uint32
	audio                   media.AudioMetadata
	color                   media.ColorMetadata
}

// Generator deterministically yields frames as RTP packets.
type Generator struct {
	specs []frameSpec
	order []int
	pos   int
}

// NewGenerator builds the full deterministic emission plan for cfg.
func NewGenerator(cfg GenConfig) *Generator {
	cfg = cfg.withDefaults()
	rng := rand.New(rand.NewSource(cfg.Seed))
	n, ki := cfg.Frames, cfg.KeyframeInterval

	raw := make([]float64, n)
	var sum float64
	for i := range raw {
		base := 1.0
		if i%ki == 0 {
			base = 3.0
		}
		raw[i] = base * (0.9 + 0.2*rng.Float64())
		sum += raw[i]
	}
	totalBits := cfg.TargetKbps * 1000 * float64(n) / 30.0
	scale := totalBits / 8 / sum

	startSeq := uint16(1000)
	startTS := uint32(90000)
	if cfg.Wrap {
		startSeq = 65530
		startTS = 0xFFFFFB00
	}

	specs := make([]frameSpec, n)
	for i := 0; i < n; i++ {
		size := uint32(raw[i] * scale)
		seq := startSeq + uint16(i)
		s := frameSpec{
			seq:      seq,
			ts:       startTS + uint32(i)*FrameTicks,
			keyframe: i%ki == 0,
			size:     size,
			declared: size,
			audio:    media.AudioMetadata{Layout: "stereo", Channels: 2, SampleRate: 48000},
			color:    media.ColorMetadata{Primaries: "bt709", Transfer: "bt709", Matrix: "bt709", Range: "limited", Present: true},
		}
		s.checksum = specCRC(cfg.Seed, seq, s.declared)
		s.payloadCRC = specCRC(cfg.Seed, seq, s.size)
		specs[i] = s
	}

	if cfg.CorruptChecksum {
		specs[n/2].checksum ^= 0x5a5a5a5a
	}
	if cfg.TruncateFrame {
		specs[n/2+5].declared = specs[n/2+5].size + 64
	}
	if cfg.BadAudioLayout {
		specs[n/3].audio = media.AudioMetadata{Layout: "octagonal", Channels: 8, SampleRate: 48000}
		specs[n/3+1].audio = media.AudioMetadata{Layout: "5.1", Channels: 2, SampleRate: 48000}
		specs[n/3+2].audio = media.AudioMetadata{Layout: "stereo", Channels: 2, SampleRate: 12345}
	}
	if cfg.ColorConflict {
		specs[n/4].color = media.ColorMetadata{Primaries: "bt2020", Transfer: "bt709", Matrix: "bt709", Range: "limited", Present: true}
		specs[n/4+1].color = media.ColorMetadata{Primaries: "bt709", Transfer: "smpte2084x", Matrix: "bt709", Range: "limited", Present: true}
		specs[n/4+2].color = media.ColorMetadata{Present: false}
	}

	// Emission order: loss burst, then reorder shuffle, then duplicates.
	lost := map[int]bool{}
	burstStart := 2*ki + 3
	for i := 0; i < cfg.LossBurst; i++ {
		lost[burstStart+i] = true
	}
	order := make([]int, 0, n)
	for i := 0; i < n; i++ {
		if !lost[i] {
			order = append(order, i)
		}
	}
	if w := cfg.ReorderWindow; w > 1 {
		for lo := 0; lo < len(order); lo += w {
			hi := lo + w
			if hi > len(order) {
				hi = len(order)
			}
			perm := rng.Perm(hi - lo)
			chunk := append([]int(nil), order[lo:hi]...)
			for j, p := range perm {
				order[lo+j] = chunk[p]
			}
		}
	}
	for d := 0; d < cfg.Duplicates; d++ {
		p := 1 + rng.Intn(len(order)-1)
		order = append(order[:p+1], append([]int{order[p]}, order[p+1:]...)...)
	}
	return &Generator{specs: specs, order: order}
}

func specCRC(seed int64, seq uint16, size uint32) uint32 {
	var b [14]byte
	binary.BigEndian.PutUint64(b[0:8], uint64(seed))
	binary.BigEndian.PutUint16(b[8:10], seq)
	binary.BigEndian.PutUint32(b[10:14], size)
	return crc32.ChecksumIEEE(b[:])
}

// Next returns the next RTP packet, or io.EOF.
func (g *Generator) Next() (*rtp.Packet, error) {
	if g.pos >= len(g.order) {
		return nil, io.EOF
	}
	s := g.specs[g.order[g.pos]]
	g.pos++
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			Marker:         s.keyframe,
			SequenceNumber: s.seq,
			Timestamp:      s.ts,
			SSRC:           StreamID,
		},
		Payload: encodePayload(&s),
	}
	return pkt, nil
}

// PacketSource yields RTP packets carrying frame records.
type PacketSource interface {
	Next() (*rtp.Packet, error)
}

// Reader decodes RTP packets into frame records.
type Reader struct {
	src PacketSource
}

func NewReader(src PacketSource) *Reader { return &Reader{src: src} }

// Next decodes the next frame, or returns io.EOF at end of input.
func (r *Reader) Next() (*media.Frame, error) {
	pkt, err := r.src.Next()
	if err != nil {
		return nil, err
	}
	f := &media.Frame{
		StreamID:  pkt.SSRC,
		Seq:       pkt.SequenceNumber,
		Timestamp: pkt.Timestamp,
		Keyframe:  pkt.Marker,
	}
	if err := decodePayload(pkt.Payload, f); err != nil {
		return nil, err
	}
	return f, nil
}
