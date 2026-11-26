// Package ingest parses locally captured RTP streams and MP4 files into
// offset-annotated records. Reads are streaming and bounded; corrupt input
// always produces a media.Error carrying the exact byte offset.
package ingest

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/pion/rtp"

	"pwf/internal/media"
)

// MaxFrameBytes bounds a single length-prefixed RTP frame so a corrupt or
// hostile length field cannot trigger unbounded allocation.
const MaxFrameBytes = 1 << 20 // 1 MiB

// ReadBufferSize is the fixed streaming read buffer: the parser never holds
// more than this much raw input at once, regardless of source length.
const ReadBufferSize = 64 * 1024 // 64 KiB

// RTPRecord is one accepted packet together with its source position and
// integral duration.
type RTPRecord struct {
	Track       string
	ExtSequence uint64
	Timestamp   uint32
	SSRC        uint32
	PayloadType uint8
	Offset      int64 // offset of the frame's length prefix in the file
	PacketBytes int
	Duration    media.Duration
	Payload     []byte
}

// GapRecord marks a missing extended sequence range. No audio is ever
// synthesized to fill a gap.
type GapRecord struct {
	Track    string
	StartSeq uint64
	Count    uint64
	AfterSeq uint64 // extended seq of the packet immediately before the gap
}

// RTPStreamResult is the full parse output for one RTP track.
type RTPStreamResult struct {
	Track      string
	Codec      string
	ClockHz    uint32
	Records    []RTPRecord
	Gaps       []GapRecord
	DupCount   int
	OutOfOrder int
}

// StreamRTP reads length-prefixed RTP packets (16-bit big-endian length,
// then one RTP packet) from r, using pion/rtp for header and sequence
// parsing. Duplicates are counted without contributing timing; missing
// sequence numbers become GapRecord entries. The first parse error is
// returned with its precise file offset.
func StreamRTP(r io.Reader, track, codec string, clockHz uint32) (*RTPStreamResult, error) {
	br := bufio.NewReaderSize(r, ReadBufferSize)
	res := &RTPStreamResult{Track: track, Codec: codec, ClockHz: clockHz}

	var (
		initialized bool
		baseExt     uint64 // extended seq of the first accepted packet
		cycles      int64  // number of 16-bit wraps relative to the first packet
		prevRaw     uint16 // raw seq of the previous accepted packet
		maxExt      uint64 // highest extended seq observed
		seen        = map[uint64]bool{}
		fileOffset  int64
	)

	for {
		var lenBuf [2]byte
		if _, err := io.ReadFull(br, lenBuf[:]); err != nil {
			if err == io.EOF {
				break // clean end of stream
			}
			return nil, media.NewError(media.KindTruncated, media.StageParseRTP,
				"truncated frame length prefix", err).WithTrack(track).AtOffset(fileOffset)
		}
		frameLen := int(binary.BigEndian.Uint16(lenBuf[:]))
		frameStart := fileOffset
		fileOffset += 2
		if frameLen == 0 {
			return nil, media.NewError(media.KindBadLength, media.StageParseRTP,
				"zero-length RTP frame", nil).WithTrack(track).AtOffset(frameStart)
		}
		if frameLen > MaxFrameBytes {
			return nil, media.NewError(media.KindBadLength, media.StageParseRTP,
				fmt.Sprintf("frame length %d exceeds bound %d", frameLen, MaxFrameBytes), nil).
				WithTrack(track).AtOffset(frameStart)
		}

		raw := make([]byte, frameLen)
		if n, err := io.ReadFull(br, raw); err != nil {
			// The error position is the first missing packet byte, i.e. just
			// past the 2-byte length prefix.
			dataStart := frameStart + 2
			return nil, media.NewError(media.KindTruncated, media.StageParseRTP,
				fmt.Sprintf("declared %d packet bytes but only %d present", frameLen, n), err).
				WithTrack(track).AtOffset(dataStart)
		}
		fileOffset += int64(frameLen)

		var pkt rtp.Packet
		if err := pkt.Unmarshal(raw); err != nil {
			// The RTP header/payload begins immediately after the prefix.
			dataStart := frameStart + 2
			return nil, media.NewError(media.KindCorrupt, media.StageParseRTP,
				"damaged RTP header or payload", err).
				WithTrack(track).AtOffset(dataStart)
		}

		if !initialized {
			initialized = true
			baseExt, cycles, prevRaw, maxExt = uint64(pkt.SequenceNumber), 0,
				pkt.SequenceNumber, uint64(pkt.SequenceNumber)
		} else {
			delta := int32(pkt.SequenceNumber) - int32(prevRaw)
			if delta > 0x8000 {
				cycles-- // raw wrapped forward past 65535 -> 0
			} else if delta < -0x8000 {
				cycles++ // late packet belonging to the previous cycle
			}
			prevRaw = pkt.SequenceNumber
		}
		ext := uint64(int64(uint64(pkt.SequenceNumber)) + (cycles << 16))
		if ext > maxExt {
			maxExt = ext
		}

		if seen[ext] {
			// Repeated packet: record it but do not count its duration twice.
			res.DupCount++
			continue
		}
		seen[ext] = true
		if ext < maxExt {
			res.OutOfOrder++
		}

		dur, err := packetDuration(codec, pkt.Payload, clockHz)
		if err != nil {
			if me, ok := media.AsError(err); ok {
				return nil, me.WithTrack(track).WithSequence(ext).AtOffset(frameStart)
			}
			return nil, media.NewError(media.KindUndetermined, media.StageParseRTP,
				err.Error(), err).WithTrack(track).WithSequence(ext).AtOffset(frameStart)
		}

		res.Records = append(res.Records, RTPRecord{
			Track:       track,
			ExtSequence: ext,
			Timestamp:   pkt.Timestamp,
			SSRC:        pkt.SSRC,
			PayloadType: pkt.PayloadType,
			Offset:      frameStart,
			PacketBytes: frameLen,
			Duration:    dur,
			Payload:     append([]byte(nil), pkt.Payload...),
		})
	}

	if !initialized {
		return nil, media.NewError(media.KindCorrupt, media.StageParseRTP,
			"stream contains no RTP packets", nil).WithTrack(track)
	}
	res.Gaps = collectGaps(track, seen, baseExt, maxExt)
	return res, nil
}

func packetDuration(codec string, payload []byte, clockHz uint32) (media.Duration, error) {
	switch codec {
	case CodecOpus:
		return OpusPacketDuration(payload)
	case CodecAAC:
		return AACPacketDuration(payload, clockHz)
	default:
		return media.Duration{}, media.NewError(media.KindUndetermined, media.StageParseRTP,
			fmt.Sprintf("unsupported codec %q", codec), nil)
	}
}

// collectGaps walks the accepted range and reports every missing seq.
func collectGaps(track string, seen map[uint64]bool, base, max uint64) []GapRecord {
	var gaps []GapRecord
	for seq := base; seq <= max; {
		if seen[seq] {
			seq++
			continue
		}
		start := seq
		for seq <= max && !seen[seq] {
			seq++
		}
		gaps = append(gaps, GapRecord{
			Track:    track,
			StartSeq: start,
			Count:    seq - start,
			AfterSeq: start - 1,
		})
	}
	return gaps
}
