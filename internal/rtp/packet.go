// Package rtp summarises the forwarding-side packet layout.  Packets are not
// transmitted; the summary proves that sequence numbers, timestamps and
// segmentation derive from the same rational timeline used by the manifest,
// and that gaps are marked rather than hidden.
package rtp

import (
	"fmt"
	"math"

	"github.com/pion/rtp"

	"regional.example/mediad/internal/clock"
	"regional.example/mediad/internal/fault"
)

// ClockRate is the RTP payload clock for linear PCM audio (RFC 3551).
const ClockRate = 48000

// DefaultFramesPerPacket is 20 ms at 48 kHz.
const DefaultFramesPerPacket = 960

// Config controls packet segmentation.
type Config struct {
	PayloadType     uint8
	FramesPerPacket int
	Channels        int
	SamplesPerByte  int // 2 for s16le
	StartSeq        uint16
	SSRC            uint32
	TimestampBase   uint32
}

// DefaultConfig returns a stereo s16le configuration.
func DefaultConfig() Config {
	return Config{
		PayloadType:     96,
		FramesPerPacket: DefaultFramesPerPacket,
		Channels:        2,
		SamplesPerByte:  2,
		SSRC:            0x13572468,
	}
}

// PacketSummary describes one packet in the plan.
type PacketSummary struct {
	SequenceNumber uint16
	Timestamp      uint32
	// StartTick and EndTick are presentation ticks at the timeline rate.
	StartTick int64
	EndTick   int64
	Frames    int
	Bytes     int
	// Marker is set on the first packet of a media run.
	Marker bool
	// GapBefore marks a packet that follows an explicit gap.
	GapBefore bool
}

// Summary is the packet plan plus aggregate statistics.
type Summary struct {
	Packets      []PacketSummary
	TotalBytes   int
	TotalFrames  int64
	NonMonotonic int
	GapsBefore   int
	// MediaTicks sums output-clock ticks backed by confirmed media only.
	MediaTicks int64
	// MissingTicks records declared gaps that were not packetised.
	MissingTicks int64
	Config       Config
}

// Generate builds the plan from a timeline.  Only Media segments produce
// packets; Gap segments become explicit holes that are never counted toward
// duration or bitrate.
func Generate(tl *clock.Timeline, cfg Config) (*Summary, error) {
	if cfg.FramesPerPacket <= 0 {
		return nil, fault.Fatal(fault.ClassAudio, "packet-frames", "frames per packet must be positive")
	}
	if cfg.Channels <= 0 || cfg.SamplesPerByte <= 0 {
		return nil, fault.Fatal(fault.ClassAudio, "packet-shape", "channels and sample size must be positive")
	}
	outRate := clock.Rate48000
	tlRate := tl.Rate
	s := &Summary{Config: cfg}
	seq := cfg.StartSeq
	framesPerPacket := int64(cfg.FramesPerPacket)
	bytesPerFrame := cfg.Channels * cfg.SamplesPerByte

	var prevEnd uint32
	havePrev := false
	for _, seg := range tl.Segments() {
		if seg.Kind == clock.Gap {
			missing, err := clock.Rescale(seg.Duration, tlRate, outRate)
			if err != nil {
				return nil, err
			}
			s.MissingTicks += missing
			havePrev = false // next media packet starts a new run
			continue
		}
		// Convert the confirmed media duration into exact output ticks.
		durTicks, err := clock.Rescale(seg.Duration, tlRate, outRate)
		if err != nil {
			return nil, err
		}
		// The segment's output-clock start, derived from its presentation tick
		// so non-zero origins carry into RTP timestamps.
		startOut, err := clock.Rescale(seg.Start, tlRate, outRate)
		if err != nil {
			return nil, err
		}
		s.MediaTicks += durTicks
		// Packets are cut in output ticks; the final one may be short only
		// when it closes the segment.
		for consumed := int64(0); consumed < durTicks; {
			frames := framesPerPacket
			if remain := durTicks - consumed; remain < framesPerPacket {
				frames = remain
			}
			relStart := consumed
			relEnd := consumed + frames
			// Report the packet's span in timeline ticks by rescaling back.
			startTick, err := clock.Rescale(startOut+relStart, outRate, tlRate)
			if err != nil {
				return nil, err
			}
			endTick, err := clock.Rescale(startOut+relEnd, outRate, tlRate)
			if err != nil {
				return nil, err
			}
			p := PacketSummary{
				SequenceNumber: seq,
				Timestamp:      cfg.TimestampBase + uint32(startOut+relStart),
				StartTick:      startTick,
				EndTick:        endTick,
				Frames:         int(frames),
				Bytes:          int(frames) * bytesPerFrame,
			}
			if !havePrev {
				p.Marker = true
				if len(s.Packets) > 0 {
					p.GapBefore = true
					s.GapsBefore++
				}
				havePrev = true
			} else if prevEnd != p.Timestamp {
				// A run should be contiguous; any jump is a gap.
				p.GapBefore = true
				p.Marker = true
				s.GapsBefore++
			}
			if len(s.Packets) > 0 && p.Timestamp < s.Packets[len(s.Packets)-1].Timestamp {
				s.NonMonotonic++
			}
			s.Packets = append(s.Packets, p)
			prevEnd = p.Timestamp + uint32(frames)
			seq++
			s.TotalBytes += p.Bytes
			s.TotalFrames += frames
			consumed += frames
		}
	}
	return s, nil
}

// ValidateTimestamps checks monotonicity and exact spacing within each run.
func (s *Summary) ValidateTimestamps() error {
	for i := 1; i < len(s.Packets); i++ {
		prev := s.Packets[i-1]
		p := s.Packets[i]
		expected := prev.Timestamp + uint32(prev.Frames)
		if p.GapBefore {
			if p.Timestamp == expected {
				return fault.New(fault.ClassAudio, "gap-unmarked",
					fmt.Sprintf("packet %d claims a gap but timestamps are contiguous", i))
			}
			continue
		}
		if p.Timestamp != expected {
			return fault.New(fault.ClassAudio, "timestamp-jump",
				fmt.Sprintf("packet %d timestamp %d != %d", i, p.Timestamp, expected))
		}
	}
	return nil
}

// ToPackets materialises pion/rtp packets.  Payloads are synthetic
// placeholders sized to the real frame count; the summary, not the payload, is
// the forwarding contract in this library.
func (s *Summary) ToPackets() []*rtp.Packet {
	out := make([]*rtp.Packet, 0, len(s.Packets))
	for _, ps := range s.Packets {
		out = append(out, &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    s.Config.PayloadType,
				SequenceNumber: ps.SequenceNumber,
				Timestamp:      ps.Timestamp,
				SSRC:           s.Config.SSRC,
				Marker:         ps.Marker,
			},
			Payload: make([]byte, ps.Bytes),
		})
	}
	return out
}

// ParsePackets round-trips generated packets through pion/rtp to prove the
// header fields survive wire encoding.
func (s *Summary) ParsePackets() ([]rtp.Packet, error) {
	raw := s.ToPackets()
	out := make([]rtp.Packet, 0, len(raw))
	for i, p := range raw {
		data, err := p.Marshal()
		if err != nil {
			return nil, fault.Fatal(fault.ClassAudio, "marshal", err.Error()).Because(err)
		}
		var decoded rtp.Packet
		if err := decoded.Unmarshal(data); err != nil {
			return nil, fault.Fatal(fault.ClassAudio, "unmarshal", err.Error()).Because(err)
		}
		if decoded.SequenceNumber != s.Packets[i].SequenceNumber || decoded.Timestamp != s.Packets[i].Timestamp {
			return nil, fault.Fatal(fault.ClassAudio, "roundtrip",
				fmt.Sprintf("packet %d header changed on the wire", i))
		}
		out = append(out, decoded)
	}
	return out, nil
}

// BitrateKbps computes the average bitrate over media duration only.
func (s *Summary) BitrateKbps() float64 {
	seconds := float64(s.MediaTicks) / ClockRate
	if seconds <= 0 {
		return 0
	}
	return float64(s.TotalBytes*8) / seconds / 1000
}

// KPSSummary returns target, actual and absolute deviation percent.
func (s *Summary) KPSSummary(targetKbps int) (target, actual, devPct float64) {
	actual = s.BitrateKbps()
	if math.IsNaN(actual) || math.IsInf(actual, 0) || actual < 0 {
		actual = 0
	}
	if targetKbps <= 0 {
		return 0, actual, 0
	}
	dev := math.Abs((actual - float64(targetKbps)) / float64(targetKbps) * 100)
	return float64(targetKbps), actual, dev
}
