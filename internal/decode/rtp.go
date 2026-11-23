package decode

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/pion/rtp"

	"medfix/internal/clock"
	"medfix/internal/timeline"
)

// RTPSink receives parsed packets in capture order plus loss reports.
type RTPSink interface {
	OnTrack(t timeline.Track)
	OnEvent(e timeline.Event)
	OnLoss(trackID string, missingSeq uint16, offset int64)
}

// ID3v2Header is the minimal fixed part of an ID3v2.2+ tag header.
type ID3v2Header struct {
	Identifier [3]byte // "ID3"
	Version    uint8
	Revision   uint8
	Flags      uint8
	Size       uint32 // syncsafe-encoded tag size (excludes header)
}

// ParseID3Header decodes an ID3v2 header from the front of payload. It
// returns the total tag size (10-byte header + syncsafe size). A truncated
// or malformed header yields a structured error carrying the input offset.
func ParseID3Header(payload []byte, offset int64) (ID3v2Header, int, error) {
	var h ID3v2Header
	if len(payload) < 10 {
		return h, 0, timeline.NewError(timeline.CodeTruncatedBox, "parse", "",
			offset, fmt.Sprintf("ID3 header needs 10 bytes, got %d", len(payload)), io.ErrUnexpectedEOF)
	}
	copy(h.Identifier[:], payload[0:3])
	if string(h.Identifier[:]) != "ID3" {
		return h, 0, timeline.NewError(timeline.CodeTruncatedBox, "parse", "",
			offset, "bad ID3 identifier", errors.New("not an ID3v2 tag"))
	}
	h.Version, h.Revision, h.Flags = payload[3], payload[4], payload[5]
	for i := 6; i < 10; i++ {
		if payload[i]&0x80 != 0 {
			return h, 0, timeline.NewError(timeline.CodeTruncatedBox, "parse", "",
				offset, "ID3 size is not syncsafe", errors.New("invalid syncsafe integer"))
		}
		h.Size = h.Size<<7 | uint32(payload[i])
	}
	total := 10 + int(h.Size)
	if len(payload) < total {
		return h, total, timeline.NewError(timeline.CodeTruncatedBox, "parse", "",
			offset, fmt.Sprintf("ID3 tag truncated: header claims %d bytes, packet has %d", total, len(payload)),
			io.ErrUnexpectedEOF)
	}
	return h, total, nil
}

// RTPParser consumes a length-prefixed RTP capture: each frame is a 4-byte
// big-endian length followed by one RTP packet (the format the project's
// own fixture generator writes). Packets stream one at a time.
type RTPParser struct {
	sourceID string
	clockRT  uint32
	unw      *clock.Unwrapper
	lastSeq  uint16
	haveSeq  bool
	seq      int
}

// NewRTPParser builds a parser for one SSRC/track at the given RTP clock
// rate (e.g. 90000 for video, 48000 for audio).
func NewRTPParser(sourceID string, clockRate uint32) *RTPParser {
	return &RTPParser{sourceID: sourceID, clockRT: clockRate, unw: clock.NewUnwrapper()}
}

// Parse reads the whole capture through r, emitting events to sink.
func (p *RTPParser) Parse(r io.Reader, sink RTPSink) error {
	track := timeline.Track{
		ID: p.sourceID + "/rtp", SourceID: p.sourceID,
		ClockRate: p.clockRT, Timescale: p.clockRT,
	}
	sink.OnTrack(track)

	var offset int64
	lenBuf := make([]byte, 4)
	for {
		n, err := io.ReadFull(r, lenBuf)
		offset += int64(n)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return timeline.NewError(timeline.CodeTruncatedBox, "parse", track.ID,
				offset, "truncated RTP length prefix", err)
		}
		frameLen := binary.BigEndian.Uint32(lenBuf)
		frame := make([]byte, frameLen)
		rd, err := io.ReadFull(r, frame)
		frameOffset := offset
		offset += int64(rd)
		if err != nil {
			return timeline.NewError(timeline.CodePacketLoss, "parse", track.ID,
				frameOffset, fmt.Sprintf("RTP frame truncated: want %d bytes", frameLen), err)
		}
		if err := p.handlePacket(frame, frameOffset, sink); err != nil {
			return err
		}
	}
}

func (p *RTPParser) handlePacket(frame []byte, offset int64, sink RTPSink) error {
	var pkt rtp.Packet
	if err := pkt.Unmarshal(frame); err != nil {
		return timeline.NewError(timeline.CodeTruncatedBox, "parse",
			p.sourceID+"/rtp", offset, "malformed RTP packet", err)
	}
	trackID := p.sourceID + "/rtp"

	if p.haveSeq {
		delta := pkt.SequenceNumber - p.lastSeq
		if delta > 1 && delta < 1<<15 {
			for miss := p.lastSeq + 1; miss != pkt.SequenceNumber; miss++ {
				sink.OnLoss(trackID, miss, offset)
			}
		}
	}
	p.haveSeq = true
	p.lastSeq = pkt.SequenceNumber

	ext, err := p.unw.Push(pkt.SequenceNumber, pkt.Timestamp, offset)
	if err != nil {
		return timeline.NewError(timeline.CodeRolloverAmbiguous, "parse", trackID,
			offset, "timestamp rollover cannot be decided", err)
	}

	kind := timeline.KindSample
	payload := bytes.Clone(pkt.Payload)
	if len(payload) >= 3 && string(payload[0:3]) == "ID3" {
		if _, _, err := ParseID3Header(payload, offset); err != nil {
			return err // truncated fields must never be silently skipped
		}
		kind = timeline.KindID3
	}
	p.seq++
	sink.OnEvent(timeline.Event{
		Kind: kind, TrackID: trackID, SourceID: p.sourceID, Seq: p.seq,
		RTPSeq: pkt.SequenceNumber, RawTS: int64(pkt.Timestamp), ExtendedTS: ext,
		// The marker bit conventionally flags frame boundaries; generated
		// fixtures set it on every packet so RTP tracks stay seekable.
		Keyframe: pkt.Marker && kind == timeline.KindSample,
		RawPayload: payload, ContentHash: hashBytes(payload),
		Offset: timeline.OffsetRange{Begin: offset, End: offset + int64(len(frame))},
	})
	return nil
}

// Rollovers exposes accepted wrap-arounds for diagnostics.
func (p *RTPParser) Rollovers() int64 { return p.unw.Rollovers() }
