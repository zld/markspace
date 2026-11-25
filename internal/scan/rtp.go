package scan

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/pion/rtp"

	"example.com/assetcheck/internal/clock"
	"example.com/assetcheck/internal/codec"
	"example.com/assetcheck/internal/fault"
	"example.com/assetcheck/internal/index"
	rtpseq "example.com/assetcheck/internal/rtp"
)

// --------------------------------------------------------------- packets --

func (s *Scanner) scanPackets(data []byte) {
	audio := s.idx.AudioTrack()
	if audio == nil {
		s.fail(fault.New(fault.StageScan, "scan.rtp.no_audio_track", fault.NoOffset, clock.Invalid,
			"no audio track available to carry the packet stream"))
		return
	}
	rate := audio.Timescale
	s.idx.RTPTrack = audio.ID
	s.idx.ClockRate = rate
	tracker := rtpseq.NewTracker()
	tsTracker := codec.NewTimestampTracker(0x80000000)
	var firstTicks uint64
	first := true
	for off := int64(0); off < int64(len(data)); {
		raw, next, err := readLengthPrefixed(data, off)
		if err != nil {
			s.fail(fault.Wrap(fault.StageScan, "scan.rtp.length_prefix", off, clock.Invalid,
				"packet length prefix or body is incomplete", err))
			return
		}
		var pkt rtp.Packet
		if err := pkt.Unmarshal(raw); err != nil {
			s.fail(fault.Wrap(fault.StageScan, "scan.rtp.malformed", off, clock.Invalid,
				"rtp packet header cannot be parsed", err))
			return
		}
		payloadOff := off + int64(len(raw)-len(pkt.Payload))
		ext := tracker.Accept(pkt.SequenceNumber, off)
		tsExt, monotonic := tsTracker.Accept(pkt.Timestamp)
		if first {
			firstTicks = tsExt
			first = false
		}
		at := mustTime(tsExt-firstTicks, rate)
		if !monotonic {
			s.fail(fault.New(fault.StageCodec, "codec.rtp.non_monotonic", off, at,
				fmt.Sprintf("rtp timestamp %d regresses at sequence %d", pkt.Timestamp, pkt.SequenceNumber)))
			off = next
			continue
		}
		for _, ev := range ext {
			switch ev.Kind {
			case rtpseq.KindPacket:
				fr, err := s.analyse(pkt.Payload, audio)
				if err != nil {
					s.fail(rebind(err, payloadOff, at))
					break
				}
				s.idx.Packets = append(s.idx.Packets, index.Packet{
					Kind:          index.PacketOK,
					TrackID:       audio.ID,
					SeqExt:        ev.SeqExt,
					Seq:           ev.Seq,
					Timestamp:     uint64(pkt.Timestamp),
					Time:          at,
					Offset:        off,
					PayloadOffset: payloadOff,
					PayloadSize:   len(pkt.Payload),
					Samples:       fr.Samples,
					Duration:      fr.Duration,
					Frames:        fr.Frames,
					Inferred:      fr.Inferred,
				})
			case rtpseq.KindDuplicate:
				s.idx.Packets = append(s.idx.Packets, index.Packet{
					Kind:          index.PacketDuplicate,
					TrackID:       audio.ID,
					SeqExt:        ev.SeqExt,
					Seq:           ev.Seq,
					Timestamp:     uint64(pkt.Timestamp),
					Time:          at,
					Offset:        off,
					PayloadOffset: payloadOff,
					PayloadSize:   len(pkt.Payload),
				})
			case rtpseq.KindGap:
				s.idx.Packets = append(s.idx.Packets, index.Packet{
					Kind:     index.PacketGap,
					TrackID:  audio.ID,
					SeqExt:   ev.SeqExt,
					Seq:      ev.Seq,
					Time:     at,
					Offset:   off,
					GapCount: ev.Count,
				})
			}
		}
		off = next
	}
	if len(s.idx.Packets) == 0 {
		s.fail(fault.New(fault.StageScan, "scan.rtp.empty", fault.NoOffset, clock.Invalid, "packet stream produced no events"))
	}
}

func (s *Scanner) analyse(payload []byte, audio *index.Track) (codec.Frame, error) {
	switch audio.Codec {
	case "aac":
		return codec.AnalyzeAAC(payload, audio.Timescale, 1024)
	default:
		return codec.AnalyzeOpus(payload)
	}
}

func readLengthPrefixed(data []byte, off int64) ([]byte, int64, error) {
	if off+4 > int64(len(data)) {
		return nil, 0, io.ErrUnexpectedEOF
	}
	n := int64(binary.BigEndian.Uint32(data[off : off+4]))
	start := off + 4
	end := start + n
	if n <= 0 || end > int64(len(data)) {
		return nil, 0, fmt.Errorf("declared packet length %d exceeds remaining %d bytes", n, int64(len(data))-start)
	}
	return data[start:end], end, nil
}

func mustTime(ticks uint64, rate uint32) clock.Nanos {
	t, err := clock.FromUnits(ticks, rate)
	if err != nil {
		return clock.Invalid
	}
	return t
}

// rebind relocates a codec fault at the packet payload offset on the shared
// time base.
func rebind(err error, payloadOff int64, at clock.Nanos) *fault.Error {
	f := fault.From(err, fault.StageCodec, "codec.packet")
	if f.Offset == fault.NoOffset {
		f.Offset = payloadOff
	}
	f.Time = at
	return f
}
