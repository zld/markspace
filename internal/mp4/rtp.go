package mp4

import (
	"encoding/binary"
	"io"
	"os"

	"github.com/pion/rtp"

	"mediacore/internal/clock"
)

// RTPMagic identifies the tiny length-prefixed RTP fixture container.
const RTPMagic = "MCRTP001"

// RTPPacket is parsed RTP header metadata plus deterministic widening.
type RTPPacket struct {
	Index              int    `json:"index"`
	RecordOffset       int64  `json:"record_offset"`
	HeaderOffset       int64  `json:"header_offset"`
	Sequence           uint16 `json:"sequence"`
	UnwrappedSequence  int64  `json:"unwrapped_sequence"`
	Timestamp          uint32 `json:"timestamp"`
	UnwrappedTimestamp int64  `json:"unwrapped_timestamp"`
	Marker             bool   `json:"marker"`
	PayloadType        uint8  `json:"payload_type"`
	SSRC               uint32 `json:"ssrc"`
	ExtensionWords     int    `json:"extension_words"`
}

// RTPTrack contains all parsed packet headers for one RTP source.
type RTPTrack struct {
	Path      string
	Number    int
	TimeScale clock.TicksPerSecond
	Packets   []RTPPacket
}

// ParseRTP reads the project's length-prefixed RTP metadata container.
func ParseRTP(path string, trackNumber int, scale clock.TicksPerSecond) (*RTPTrack, error) {
	if err := scale.Validate(path, trackNumber); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, clock.Wrap(clock.CategoryIO, path, trackNumber, 0, "open rtp", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, clock.Wrap(clock.CategoryIO, path, trackNumber, 0, "stat rtp", err)
	}
	var magic [8]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil || string(magic[:]) != RTPMagic {
		return nil, clock.New(clock.CategoryInvalidRTP, path, trackNumber, 0, "missing RTP fixture magic")
	}
	track := &RTPTrack{Path: path, Number: trackNumber, TimeScale: scale}
	var lastSeq int64
	var lastTS int64
	have := false
	pos := int64(8)
	for pos < st.Size() {
		recordOff := pos
		var lenBuf [4]byte
		if _, err := io.ReadFull(f, lenBuf[:]); err != nil {
			return nil, clock.New(clock.CategoryTruncatedRTP, path, trackNumber, recordOff,
				"truncated RTP record length")
		}
		packetLen := binary.BigEndian.Uint32(lenBuf[:])
		pos += 4
		if packetLen < 12 || packetLen > 1<<20 || pos+int64(packetLen) > st.Size() {
			return nil, clock.New(clock.CategoryTruncatedRTP, path, trackNumber, recordOff,
				"RTP packet length extends past file")
		}
		raw := make([]byte, packetLen)
		if _, err := io.ReadFull(f, raw); err != nil {
			return nil, clock.New(clock.CategoryTruncatedRTP, path, trackNumber, recordOff,
				"truncated RTP packet")
		}
		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(raw); err != nil {
			return nil, clock.Wrap(clock.CategoryInvalidRTP, path, trackNumber, recordOff,
				"pion/rtp failed to parse header", err)
		}
		seq := widenSequence(lastSeq, pkt.SequenceNumber, have)
		ts, err := widenTimestamp(path, trackNumber, recordOff+4, lastTS, pkt.Timestamp, have)
		if err != nil {
			return nil, err
		}
		lastSeq, lastTS, have = seq, ts, true
		track.Packets = append(track.Packets, RTPPacket{
			Index: len(track.Packets), RecordOffset: recordOff, HeaderOffset: recordOff + 4,
			Sequence: pkt.SequenceNumber, UnwrappedSequence: seq,
			Timestamp: pkt.Timestamp, UnwrappedTimestamp: ts,
			Marker: pkt.Marker, PayloadType: pkt.PayloadType, SSRC: pkt.SSRC,
			ExtensionWords: len(pkt.Extensions),
		})
		pos += int64(packetLen)
	}
	if len(track.Packets) == 0 {
		return nil, clock.New(clock.CategoryInvalidRTP, path, trackNumber, 8, "RTP source has no packets")
	}
	return track, nil
}

func widenSequence(prev int64, value uint16, have bool) int64 {
	cur := int64(value)
	if !have {
		return cur
	}
	// Choose the nearest 16-bit epoch. This is deterministic for ordered fixtures.
	for cur < prev-(1<<15) {
		cur += 1 << 16
	}
	for cur > prev+(1<<15) {
		cur -= 1 << 16
	}
	return cur
}

func widenTimestamp(path string, track int, offset int64, prev int64, value uint32, have bool) (int64, error) {
	cur := int64(value)
	if !have {
		return cur, nil
	}
	rawDelta := int64(uint32(cur) - uint32(prev))
	if rawDelta == 1<<31 {
		return 0, clock.New(clock.CategoryTimestampWrap, path, track, offset,
			"RTP timestamp jump is exactly equidistant across 32-bit wrap; refusing silent correction")
	}
	if rawDelta < 1<<31 {
		return prev + rawDelta, nil
	}
	// Modular delta is in the upper half, meaning a small backward delta. Treat
	// the forward wrap case only when it is clearly closer to +2^32.
	forward := rawDelta
	backward := rawDelta - (1 << 32)
	if -backward < forward {
		// Reordered/backward timestamp: preserve it as an explicit negative delta
		// rather than wrapping it upward.
		return prev + backward, nil
	}
	return prev + forward, nil
}
