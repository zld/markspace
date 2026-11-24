// Package fixture generates all sample data in-process: no external files are
// ever read. The generator is fully deterministic for a given seed and spec,
// which is what lets two demo runs with the same seed produce byte-identical
// output. It can inject the exact fault classes the acceptance script checks:
// truncation, payload corruption (with its precise stream offset), sequence
// rollover, in-window reordering and out-of-window late packets.
package fixture

import (
	"bytes"
	"fmt"
	"math/rand"

	"github.com/pion/rtp"

	mediartp "mediapipeline/internal/rtp"
)

// Default audio parameters shared by demo and verification fixtures.
const (
	OpusRate     uint32 = 48000
	OpusSamples  uint16 = 960 // 20 ms
	VideoRate    uint32 = 90000
	VideoStep    uint32 = 3000 // 30 fps in 90 kHz units
	DefaultBlock uint16 = 1
	AudioSSRC    uint32 = 0xA1A1A1A1
	VideoSSRC    uint32 = 0x56494431 // "VID1"
)

// wireItem is one logical packet waiting for wire-order placement.
type wireItem struct {
	idx int
	pkt *rtp.Packet
}

// Spec describes one generated audio stream and its injected faults.
type Spec struct {
	Seed       int64
	Packets    int
	SampleRate uint32
	StartSeq   uint16
	BlockSize  uint16
	// Drop removes logical packets (index-based) from the wire.
	Drop map[int]bool
	// Duplicate emits the logical packet a second time.
	Duplicate map[int]bool
	// ReorderSwap swaps wire positions of logical packets i and i+1.
	ReorderSwap map[int]bool
	// Corrupt maps a logical packet to a frame-payload byte to flip.
	Corrupt map[int]int
	// Late moves logical packet i that many wire positions toward the tail,
	// beyond the jitter window when the distance exceeds the window.
	Late map[int]int
	// TruncateAt cuts the stream at this absolute byte (0 disables).
	TruncateAt int
}

// Meta describes what was generated, including exact fault offsets.
type Meta struct {
	Packets    int
	Duplicates int
	Dropped    []int
	// Offsets maps logical packet index to its first stream byte.
	Offsets map[int]int64
	Samples []uint16
	// CorruptOffsets maps logical packet to absolute corrupted stream byte.
	CorruptOffsets map[int]int64
	// LatePackets lists logical indices moved outside normal order.
	LatePackets []int
}

// VideoMeta describes a generated video stream.
type VideoMeta struct {
	Frames    int
	Timestamps []uint32
	Offsets   map[int]int64
}

// GenerateAudio builds a length-prefixed RTP audio stream per spec.
func GenerateAudio(s Spec) ([]byte, Meta, error) {
	if s.Packets <= 0 || s.Packets > mediartp.MaxPackets {
		return nil, Meta{}, fmt.Errorf("fixture: packet count %d out of range", s.Packets)
	}
	if s.SampleRate == 0 {
		s.SampleRate = OpusRate
	}
	if s.BlockSize == 0 {
		s.BlockSize = DefaultBlock
	}
	rng := rand.New(rand.NewSource(s.Seed))
	meta := Meta{Packets: s.Packets, Offsets: map[int]int64{}, Samples: make([]uint16, s.Packets),
		CorruptOffsets: map[int]int64{}}

	// Build logical packets, then apply ordering faults on the wire sequence.
	items := make([]wireItem, 0, s.Packets)
	var pts uint32
	for i := 0; i < s.Packets; i++ {
		// Variable frame length: alternate 20 ms and 10 ms Opus frames.
		samples := OpusSamples
		if i%2 == 1 {
			samples = OpusSamples / 2
		}
		meta.Samples[i] = samples
		dataLen := 8 + rng.Intn(8) // bounded, deterministic payload length
		data := make([]byte, dataLen)
		for j := range data {
			data[j] = byte(rng.Intn(256))
		}
		frame := mediartp.BuildMediaFrame(0, samples, s.BlockSize, data)
		seq := uint16(int(s.StartSeq) + i)
		p := &rtp.Packet{
			Header:  rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: seq, Timestamp: pts, SSRC: AudioSSRC},
			Payload: frame,
		}
		pts += uint32(samples)
		items = append(items, wireItem{idx: i, pkt: p})
	}

	// Reordering swaps first, then late moves operate on the wire slice.
	wire := append([]wireItem(nil), items...)
	for i := range wire {
		if s.ReorderSwap[i] && i+1 < len(wire) {
			wire[i], wire[i+1] = wire[i+1], wire[i]
		}
	}
	for idx, shift := range s.Late {
		from := indexOf(wire, idx)
		to := from + shift
		if to >= len(wire) {
			to = len(wire) - 1
		}
		if from >= 0 && from != to {
			el := wire[from]
			rest := append([]wireItem(nil), wire[:from]...)
			rest = append(rest, wire[from+1:]...)
			// `to` indexes the post-removal slice; insert without aliasing.
			wire = make([]wireItem, 0, len(rest)+1)
			wire = append(wire, rest[:to]...)
			wire = append(wire, el)
			wire = append(wire, rest[to:]...)
			meta.LatePackets = append(meta.LatePackets, idx)
		}
	}

	var buf bytes.Buffer
	for _, it := range wire {
		if s.Drop[it.idx] {
			meta.Dropped = append(meta.Dropped, it.idx)
			continue
		}
		raw, err := it.pkt.Marshal()
		if err != nil {
			return nil, Meta{}, err
		}
		frameOff := len(raw) - len(it.pkt.Payload)
		pktStart := int64(buf.Len())
		meta.Offsets[it.idx] = pktStart
		if off, ok := s.Corrupt[it.idx]; ok {
			meta.CorruptOffsets[it.idx] = pktStart + 2 + int64(frameOff) + int64(off)
		}
		if err := mediartp.WriteStreamPacket(&buf, it.pkt); err != nil {
			return nil, Meta{}, err
		}
		if s.Duplicate[it.idx] {
			if err := mediartp.WriteStreamPacket(&buf, it.pkt); err != nil {
				return nil, Meta{}, err
			}
			meta.Duplicates++
		}
	}
	out := buf.Bytes()
	if s.TruncateAt > 0 && s.TruncateAt < len(out) {
		out = out[:s.TruncateAt]
	}
	return out, meta, nil
}

// GenerateVideo builds a length-prefixed RTP video stream (90 kHz clock).
func GenerateVideo(seed int64, frames int, startSeq uint16) ([]byte, VideoMeta, error) {
	if frames <= 0 || frames > mediartp.MaxPackets {
		return nil, VideoMeta{}, fmt.Errorf("fixture: frame count %d out of range", frames)
	}
	rng := rand.New(rand.NewSource(seed + 99))
	meta := VideoMeta{Frames: frames, Timestamps: make([]uint32, frames), Offsets: map[int]int64{}}
	var buf bytes.Buffer
	for i := 0; i < frames; i++ {
		ts := uint32(i) * VideoStep
		meta.Timestamps[i] = ts
		data := make([]byte, 16+rng.Intn(16))
		for j := range data {
			data[j] = byte(rng.Intn(256))
		}
		frame := mediartp.BuildMediaFrame(0, uint16(VideoStep/1000), DefaultBlock, data)
		p := &rtp.Packet{
			Header:  rtp.Header{Version: 2, PayloadType: 97, SequenceNumber: startSeq + uint16(i), Timestamp: ts, SSRC: VideoSSRC},
			Payload: frame,
		}
		meta.Offsets[i] = int64(buf.Len())
		if err := mediartp.WriteStreamPacket(&buf, p); err != nil {
			return nil, VideoMeta{}, err
		}
	}
	return buf.Bytes(), meta, nil
}

func indexOf(wire []wireItem, idx int) int {
	for i, it := range wire {
		if it.idx == idx {
			return i
		}
	}
	return -1
}
