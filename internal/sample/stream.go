package sample

import (
	"encoding/binary"
	"fmt"
	"math/rand"

	"github.com/pion/rtp"

	"medcheck/internal/adapter"
	"medcheck/internal/clock"
)

// SlotSize is the fixed source boundary spacing used by fixture offsets.
const SlotSize int64 = 256

const (
	AudioSSRC uint32 = 0x11111111
	VideoSSRC uint32 = 0x22222222
	AudioPT   uint8  = 97
	VideoPT   uint8  = 96
)

// Packet is one generated RTP packet and its ground-truth boundary.
type Packet struct {
	Global     int
	Order      int
	Stream     clock.StreamID
	SSRC       uint32
	Seq        uint16
	ExtSeq     uint64
	RTPTS      uint32
	PTSNS      int64
	DurationNS int64
	ArrivalNS  int64
	Offset     int64
	Raw        []byte
	Keyframe   bool
	Lost       bool
	Invalid    bool
	Color      adapter.ColorMetadata
}

// Config controls deterministic fixture generation.
type Config struct {
	Seed        int64
	PacketCount int
	Loss        []int
	Reorder     int
	CorruptAt   []int
}

// Sample contains generated packets and stream configuration.
type Sample struct {
	Config         Config
	Packets        []*Packet // observed arrival order
	All            []*Packet // canonical global order, including losses
	Audio          adapter.AudioSpec
	Video          adapter.VideoSpec
	RTCP           []byte
	FirstBadOffset int64
	VideoOriginNS  int64
}

// Generate builds a local RTP/RTCP-like fixture without external assets.
func Generate(cfg Config) (*Sample, error) {
	if cfg.PacketCount <= 0 {
		cfg.PacketCount = 25
	}
	rng := rand.New(rand.NewSource(cfg.Seed))
	lost := make(map[int]bool, len(cfg.Loss))
	for _, i := range cfg.Loss {
		if i < 0 || i >= cfg.PacketCount {
			return nil, fmt.Errorf("sample: loss index %d out of range 0..%d", i, cfg.PacketCount-1)
		}
		lost[i] = true
	}
	corrupt := make(map[int]bool, len(cfg.CorruptAt))
	for _, i := range cfg.CorruptAt {
		corrupt[i] = true
	}

	s := &Sample{Config: cfg, VideoOriginNS: 120_000_000, FirstBadOffset: -1}
	s.Audio = adapter.AudioSpec{
		Version: adapter.VersionAACV1, Codec: "AAC", Channels: 2, Layout: "stereo",
		SampleRate: 48000, ChannelMap: []string{"FL", "FR"},
	}
	s.Video = adapter.VideoSpec{
		Version: adapter.VersionH264V1, Codec: "H.264", Width: 640, Height: 360,
		Color: adapter.ColorMetadata{
			Present: true, Primaries: 1, Transfer: 1, Matrix: 1,
			Source: "bitstream:vui",
		},
	}

	audioSeq, videoSeq := uint16(100), uint16(500)
	audioFrame, videoFrame := uint32(0), uint32(0)
	for i := 0; i < cfg.PacketCount; i++ {
		isAudio := i%5 == 0 || i%5 == 2 || i%5 == 4
		p := &Packet{Global: i, Offset: int64(i) * SlotSize, Lost: lost[i], Invalid: corrupt[i]}
		if lost[i] && (s.FirstBadOffset < 0 || p.Offset < s.FirstBadOffset) {
			s.FirstBadOffset = p.Offset
		}
		if isAudio {
			p.Stream, p.SSRC, p.Seq, p.ExtSeq = clock.StreamAudio, AudioSSRC, audioSeq, uint64(audioSeq)
			p.RTPTS = audioFrame * 960
			p.PTSNS = int64(audioFrame) * 20_000_000
			p.DurationNS = 20_000_000
			p.Raw = mustRTP(rng, AudioPT, p.SSRC, p.Seq, p.RTPTS, []byte(fmt.Sprintf("audio-%02d", audioFrame)))
			audioSeq++
			audioFrame++
		} else {
			p.Stream, p.SSRC, p.Seq, p.ExtSeq = clock.StreamVideo, VideoSSRC, videoSeq, uint64(videoSeq)
			p.RTPTS = videoFrame * 3000
			p.PTSNS = s.VideoOriginNS + int64(videoFrame)*33_333_333
			p.DurationNS = 33_333_333
			p.Keyframe = videoFrame%2 == 0
			p.Color = s.Video.Color
			payload := []byte{0, 0, 0, 1}
			if p.Keyframe {
				payload = append(payload, 0x67, 0x42, 0x00, 0x1e)
			} else {
				payload = append(payload, 0x41)
			}
			payload = append(payload, byte(i), byte(rng.Intn(256)))
			p.Raw = mustRTP(rng, VideoPT, p.SSRC, p.Seq, p.RTPTS, payload)
			videoSeq++
			videoFrame++
		}
		if corrupt[i] {
			// A damaged RTP version byte is detected at this exact boundary.
			p.Raw[0] &^= 0xc0
		}
		s.All = append(s.All, p)
		if !p.Lost {
			s.Packets = append(s.Packets, p)
		}
	}

	swapDisjointAudioPairs(s.Packets, cfg.Reorder)
	report := clock.RTCPReport{SSRC: VideoSSRC, NTPNS: s.VideoOriginNS, RTPTimestamp: 0}
	s.RTCP = clock.MarshalSenderReport(report)
	for order, p := range s.Packets {
		p.Order = order
		p.ArrivalNS = int64(order) * 10_000_000
	}
	return s, nil
}

func mustRTP(rng *rand.Rand, pt uint8, ssrc uint32, seq uint16, ts uint32, payload []byte) []byte {
	// Add a deterministic seed-derived tail without affecting sequence fields.
	payload = append(payload, byte(rng.Intn(256)), byte(rng.Intn(256)))
	b, err := (&rtp.Packet{Header: rtp.Header{
		Version: 2, PayloadType: pt, SSRC: ssrc, SequenceNumber: seq, Timestamp: ts,
	}, Payload: payload}).Marshal()
	if err != nil {
		panic(err)
	}
	return b
}

func swapDisjointAudioPairs(packets []*Packet, pairs int) {
	var audioPositions []int
	for i, p := range packets {
		if p.Stream == clock.StreamAudio {
			audioPositions = append(audioPositions, i)
		}
	}
	// Start after the first audio packet so startup sequence discovery does
	// not mistake the preceding late packet for pre-session history.
	const start = 1
	for k := 0; k < pairs; k++ {
		left := start + 2*k
		if left+1 >= len(audioPositions) {
			break
		}
		i := audioPositions[left]
		j := audioPositions[left+1]
		packets[i], packets[j] = packets[j], packets[i]
	}
}

// EncodeFixedSlots writes packets in 256-byte boundary slots for offset tests.
func (s *Sample) EncodeFixedSlots() []byte {
	total := s.Config.PacketCount * int(SlotSize)
	out := make([]byte, total)
	for _, p := range s.All {
		if p.Lost {
			continue
		}
		binary.BigEndian.PutUint16(out[p.Offset:p.Offset+2], uint16(len(p.Raw)))
		copy(out[p.Offset+2:], p.Raw)
	}
	return out
}
