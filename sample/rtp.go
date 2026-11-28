package sample

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pion/rtp"
)

// RTPStream is a generated RTP fixture: packet bytes in wire order plus what the
// window must report about them.
type RTPStream struct {
	// Name is the file name inside the sample directory.
	Name string
	// Path is the absolute path of the written packet file.
	Path string
	// Data is the packet file: 4-byte big-endian length then the packet.
	Data []byte
	// Packets is the number of packets written.
	Packets int
	// Lost is the number of packets dropped on purpose.
	Lost int
	// Duplicated is the number of packets replayed.
	Duplicated int
	// Reordered is the number of packets held back and re-sent later.
	Reordered int
	// ExpectedGaps is the number of distinct missing runs the window should
	// declare.
	ExpectedGaps int
	// ExpectedJumps is the number of timestamp discontinuities a jump-aware
	// window should report.
	ExpectedJumps int
	// FirstSeq and LastSeq bound the original sequence numbers.
	FirstSeq uint16
	LastSeq  uint16
}

// RTPOptions controls the generated stream.
type RTPOptions struct {
	// Packets is the number of original packets.
	Packets int
	// LossEvery drops every Nth packet. Zero disables loss.
	LossEvery int
	// ReorderEvery swaps each Nth packet with its successor. Zero disables
	// reordering.
	ReorderEvery int
	// DuplicateEvery replays every Nth packet. Zero disables duplication.
	DuplicateEvery int
	// StartSeq is the first sequence number; values near 65535 exercise the
	// 16-bit wraparound.
	StartSeq uint16
	// StartTimestamp is the first RTP timestamp.
	StartTimestamp uint32
	// Phase offsets the loss, reorder and duplicate cadence, so different
	// seeds produce different but still deterministic damage patterns.
	Phase int
	// TimestampJumpAt inserts a discontinuity before this packet index. A
	// negative value disables it.
	TimestampJumpAt int
	// TimestampJumpTicks is the size of the inserted discontinuity in RTP
	// ticks; zero means five packet durations.
	TimestampJumpTicks int64
	// ClockRate is the RTP clock in Hz; zero means 90000.
	ClockRate uint32
	// PacketDurationNS is the media duration per packet; zero means 20 ms.
	PacketDurationNS int64
}

// BuildRTP generates an RTP stream file. Packets are serialised with pion/rtp's
// packet marshaller in wire order.
func BuildRTP(dir string, name string, opts RTPOptions) (RTPStream, error) {
	if opts.Packets <= 0 {
		opts.Packets = 60
	}
	if opts.ClockRate == 0 {
		opts.ClockRate = 90000
	}
	if opts.PacketDurationNS <= 0 {
		opts.PacketDurationNS = 20_000_000
	}
	tickStep := uint32(opts.PacketDurationNS * int64(opts.ClockRate) / 1_000_000_000)
	stream := RTPStream{Name: name, FirstSeq: opts.StartSeq}

	type wirePacket struct {
		bytes []byte
	}
	var wire []wirePacket
	dropped := map[int]bool{}
	for i := 0; i < opts.Packets; i++ {
		seq := opts.StartSeq + uint16(i)
		timestamp := opts.StartTimestamp + uint32(i)*tickStep
		// A clock discontinuity shifts every later timestamp; it is a step in
		// the media clock, not a one-packet blip.
		if opts.TimestampJumpAt >= 0 && i >= opts.TimestampJumpAt {
			jumpTicks := opts.TimestampJumpTicks
			if jumpTicks == 0 {
				jumpTicks = int64(tickStep) * 5
			}
			timestamp += uint32(jumpTicks)
		}
		packet := &rtp.Packet{Header: rtp.Header{
			Version:        2,
			PayloadType:    111,
			SequenceNumber: seq,
			Timestamp:      timestamp,
			SSRC:           0x0BADCAFE,
			Marker:         i == opts.Packets-1,
		}}
		payload := make([]byte, 160)
		for j := range payload {
			payload[j] = byte((i*31 + j*7) % 251)
		}
		packet.Payload = payload
		raw, err := packet.Marshal()
		if err != nil {
			return RTPStream{}, fmt.Errorf("marshal packet %d: %w", i, err)
		}
		if opts.LossEvery > 0 && (i+opts.Phase)%opts.LossEvery == opts.LossEvery-1 && i != opts.Packets-1 {
			dropped[i] = true
			stream.Lost++
			continue
		}
		wire = append(wire, wirePacket{bytes: raw})
	}
	if opts.ReorderEvery > 0 {
		for i := 0; i+1 < len(wire); i++ {
			if (i+opts.Phase)%opts.ReorderEvery == opts.ReorderEvery-1 {
				wire[i], wire[i+1] = wire[i+1], wire[i]
				stream.Reordered++
			}
		}
	}
	if opts.DuplicateEvery > 0 {
		var withDup []wirePacket
		for i, packet := range wire {
			withDup = append(withDup, packet)
			if (i+1+opts.Phase)%opts.DuplicateEvery == 0 {
				withDup = append(withDup, packet)
				stream.Duplicated++
			}
		}
		wire = withDup
	}
	// Count the missing runs the original sequence line leaves behind.
	stream.ExpectedGaps = countRuns(opts.Packets, dropped)
	// Exactly one discontinuity is reported: the packets after it keep the new
	// clock offset, so their own spacing is uniform again.
	if opts.TimestampJumpAt >= 0 && opts.TimestampJumpAt < opts.Packets {
		stream.ExpectedJumps = 1
		if opts.TimestampJumpAt == 0 {
			// A jump before the first packet just sets the base clock.
			stream.ExpectedJumps = 0
		}
	}
	var data []byte
	for _, packet := range wire {
		length := make([]byte, 4)
		binary.BigEndian.PutUint32(length, uint32(len(packet.bytes)))
		data = append(data, length...)
		data = append(data, packet.bytes...)
	}
	stream.Data = data
	stream.Packets = len(wire)
	stream.LastSeq = opts.StartSeq + uint16(opts.Packets-1)
	stream.Path = filepath.Join(dir, name)
	if err := os.WriteFile(stream.Path, data, 0o644); err != nil {
		return RTPStream{}, fmt.Errorf("write %s: %w", name, err)
	}
	return stream, nil
}

// countRuns counts contiguous runs of dropped indices.
func countRuns(total int, dropped map[int]bool) int {
	runs := 0
	inRun := false
	for i := 0; i < total; i++ {
		if dropped[i] {
			if !inRun {
				runs++
				inRun = true
			}
			continue
		}
		inRun = false
	}
	return runs
}
