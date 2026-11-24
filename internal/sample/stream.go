package sample

import (
	"os"
	"path/filepath"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"

	"mediapipe/internal/audio"
	"mediapipe/internal/clock"
	"mediapipe/internal/stream"
)

const (
	DefaultFrames     = 600 // 12 seconds at 20ms
	DefaultClockRate  = 8000
	DefaultFrameMs    = 20
	DefaultSamples    = 72 // 16 byte header + 144 bytes = 160 RTP payload bytes
	FixedWallOriginMs = 1_700_000_000_000
)

type Options struct {
	OutputPath     string
	Frames         int
	ClockRate      uint32
	FrameMs        int64
	StartSequence  uint16
	StartTimestamp uint32
}

func DefaultOptions(path string) Options {
	return Options{
		OutputPath:     path,
		Frames:         DefaultFrames,
		ClockRate:      DefaultClockRate,
		FrameMs:        DefaultFrameMs,
		StartSequence:  65530, // exercises 16-bit wrap in the demo stream
		StartTimestamp: 0xffffff00,
	}
}

// Amplitude is deterministic and intentionally repeats motif 0 in segment 3.
func Amplitude(frame int) int16 {
	relative := frame % 100
	segment := frame / 100
	if segment == 3 {
		return AmplitudeForMotif(0, relative)
	}
	return AmplitudeForMotif(segment, relative)
}

func AmplitudeForMotif(seed, relative int) int16 {
	if relative >= 90 { // final two 20ms windows are silence
		return 0
	}
	pattern := []int16{900, 1500, 2100, 1600, 1100}
	base := pattern[(relative/4+seed)%len(pattern)]
	if relative%17 == 0 {
		base += 500
	}
	if seed%3 == 0 && relative < 12 {
		base = 1300
	}
	return base
}

func buildPacket(o Options, frame int) *rtp.Packet {
	samples := make([]int16, DefaultSamples)
	amp := Amplitude(frame)
	for i := range samples {
		samples[i] = amp + int16((i%7)-3)*4
	}
	return &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			Padding:        false,
			Marker:         frame%100 == 99,
			PayloadType:    96,
			SequenceNumber: uint16(uint32(o.StartSequence) + uint32(frame)),
			Timestamp:      o.StartTimestamp + uint32(frame)*uint32(o.FrameMs*8),
			SSRC:           0x5eed01,
		},
		Payload: audio.EncodePayload(samples, o.ClockRate),
	}
}

// Generate writes the deterministic healthy-but-impaired sample stream.
func Generate(o Options) error {
	if o.Frames <= 0 {
		o.Frames = DefaultFrames
	}
	if o.ClockRate == 0 {
		o.ClockRate = DefaultClockRate
	}
	if o.FrameMs == 0 {
		o.FrameMs = DefaultFrameMs
	}
	if err := os.MkdirAll(filepath.Dir(o.OutputPath), 0o755); err != nil {
		return err
	}
	f, err := os.Create(o.OutputPath)
	if err != nil {
		return err
	}
	defer f.Close()

	w, err := stream.NewWriter(f, o.ClockRate)
	if err != nil {
		return err
	}
	packets := make([]*rtp.Packet, o.Frames)
	for i := range packets {
		packets[i] = buildPacket(o, i)
	}

	lost := map[int]bool{200: true, 450: true}
	heldLate := 160
	swaps := map[int]bool{20: true, 140: true, 260: true, 420: true}
	order := make([]int, 0, o.Frames)
	for i := 0; i < o.Frames; {
		if lost[i] || i == heldLate {
			i++
			continue
		}
		if swaps[i] && i+1 < o.Frames {
			order = append(order, i+1, i)
			i += 2
			continue
		}
		order = append(order, i)
		i++
	}

	writeSR := func(frame int) error {
		p := packets[frame]
		_, err := w.WriteRTCP(&rtcp.SenderReport{
			SSRC:        0x5eed01,
			NTPTime:     clock.UnixMilliToNTP(FixedWallOriginMs + int64(frame)*o.FrameMs),
			RTPTime:     p.Timestamp,
			PacketCount: uint32(frame + 1),
			OctetCount:  uint32((frame + 1) * len(p.Payload)),
		})
		return err
	}

	for _, idx := range order {
		if idx%100 == 0 {
			if err := writeSR(idx); err != nil {
				return err
			}
		}
		if _, err := w.WriteRTP(packets[idx]); err != nil {
			return err
		}
		if idx == 50 || idx == 350 {
			if _, err := w.WriteRTP(packets[idx]); err != nil { // exact duplicate
				return err
			}
		}
		if idx == 192 {
			if _, err := w.WriteRTP(packets[heldLate]); err != nil { // outside window
				return err
			}
		}
	}
	return nil
}
