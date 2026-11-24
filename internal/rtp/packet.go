// Package rtp parses the bounded, fixture-generated RTP audio stream and turns
// 16-bit RTP sequence numbers into monotonic extended sequence numbers.
//
// On top of the standard RTP header (parsed by github.com/pion/rtp) the
// payload is a fixture "media frame": a small header followed by sample data
// and a per-block CRC32 table. The per-block table lets verification locate a
// corrupted byte to exactly one block (1-byte blocks by default), satisfying
// the <=1 byte localization requirement without any codec implementation.
package rtp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"

	"github.com/pion/rtp"

	mpierr "mediapipeline/internal/errors"
)

// Hard limits. Inputs beyond these are rejected with CatLimit.
const (
	MaxPackets       = 100000
	MaxPacketBytes   = 65535
	MaxFramePayload  = 8192
	frameMagic0      = 'M'
	frameMagic1      = 'P'
	frameHeaderSize  = 10 // magic2 ver1 flags1 samples2 blocksize2 blocks2
	blockCheckSize   = 4  // one CRC32 per block
)

// Frame is one parsed, verified media frame plus its RTP metadata.
type Frame struct {
	// ExtSeq is the 64-bit extended sequence number (cycles<<16 | seq).
	ExtSeq uint64
	// Duplicate is true when this extended sequence was seen before.
	Duplicate bool
	// Seq16 is the original RTP sequence number.
	Seq16 uint16
	// Timestamp is the raw RTP timestamp in the sender clock domain.
	Timestamp uint32
	// SSRC is the RTP synchronization source identifier.
	SSRC uint32
	// SampleRate is the payload's native sample rate in Hz.
	SampleRate uint32
	// Samples is the number of audio samples carried (variable length).
	Samples uint16
	// Payload is the verified raw media data.
	Payload []byte
	// StreamOffset is the byte offset of the RTP packet's first byte in the
	// input stream.
	StreamOffset int64
	// FrameOffset is the byte offset of the frame header inside the payload.
	FrameOffset int64
	// BlockSize is the checksum granularity in payload bytes.
	BlockSize uint16
}

// Classification result of feeding one sequence number to the Tracker.
type TrackResult struct {
	// Seq is the extended sequence number assigned to the packet.
	Seq uint64
	// Duplicate reports that this extended sequence was seen before.
	Duplicate bool
}

// Tracker extends 16-bit RTP sequence numbers and detects duplicates,
// reordering, rollover and gaps. It keeps no packet content, only state.
type Tracker struct {
	initialized bool
	baseExt     uint64 // extended seq of the first packet
	lastExt     uint64 // highest extended seq ever observed
	maxSeq16    uint16
	rollovers   uint64
	seen        map[uint64]struct{}
}

// NewTracker creates an empty Tracker.
func NewTracker() *Tracker {
	return &Tracker{seen: make(map[uint64]struct{})}
}

// Push feeds one raw 16-bit sequence number.
func (t *Tracker) Push(seq16 uint16) TrackResult {
	if !t.initialized {
		t.initialized = true
		t.baseExt = uint64(seq16)
		t.lastExt = uint64(seq16)
		t.maxSeq16 = seq16
		t.seen[t.lastExt] = struct{}{}
		return TrackResult{Seq: t.lastExt}
	}
	ext := t.extend(seq16)
	if _, dup := t.seen[ext]; dup {
		return TrackResult{Seq: ext, Duplicate: true}
	}
	t.seen[ext] = struct{}{}
	if ext > t.lastExt {
		t.lastExt = ext
	}
	return TrackResult{Seq: ext}
}

// extend maps a raw sequence number to an extended one using the distance to
// the most recent maximum, following the RFC 3550 rollover heuristic.
func (t *Tracker) extend(seq16 uint16) uint64 {
	const maxDist = 1 << 15
	dist := int32(seq16) - int32(t.maxSeq16)
	switch {
	case dist > maxDist:
		// Far ahead numerically means it is from the previous cycle.
		return (t.rollovers - 1)<<16 | uint64(seq16)
	case dist < -maxDist:
		// Far behind numerically means it is from the next cycle.
		t.rollovers++
		t.maxSeq16 = seq16
		return t.rollovers<<16 | uint64(seq16)
	default:
		if dist > 0 {
			t.maxSeq16 = seq16
		}
		return t.rollovers<<16 | uint64(seq16)
	}
}

// LastExt returns the highest extended sequence number observed so far.
func (t *Tracker) LastExt() uint64 { return t.lastExt }

// BaseExt returns the extended sequence number of the first packet.
func (t *Tracker) BaseExt() uint64 { return t.baseExt }

// Reader pulls length-prefixed RTP packets from a byte stream and verifies
// each frame's per-block checksums. All byte offsets are stream-relative.
type Reader struct {
	r          io.Reader
	tracker    *Tracker
	sampleRate uint32
	count      int
	duplicates int
	streamPos  int64
}

// NewReader wraps r. sampleRate is the native rate of every frame's payload.
func NewReader(r io.Reader, sampleRate uint32) *Reader {
	return &Reader{r: r, tracker: NewTracker(), sampleRate: sampleRate}
}

// Tracker exposes the sequence tracker (e.g. for test assertions).
func (rd *Reader) Tracker() *Tracker { return rd.tracker }

// Count returns the number of packets read so far.
func (rd *Reader) Count() int { return rd.count }

// Duplicates returns how many packets had a repeated extended sequence.
func (rd *Reader) Duplicates() int { return rd.duplicates }

// Read returns the next verified frame, or io.EOF at a clean end of stream.
func (rd *Reader) Read() (*Frame, error) {
	var lenBuf [2]byte
	pktStart := rd.streamPos
	if _, err := io.ReadFull(rd.r, lenBuf[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		return nil, mpierr.New(mpierr.CatTruncated, rd.tracker.LastExt()+1, "",
			"stream ended inside packet length prefix").AtOffset(rd.streamPos).Wrap(err)
	}
	pktLen := int(binary.BigEndian.Uint16(lenBuf[:]))
	rd.streamPos += 2
	if pktLen == 0 || pktLen > MaxPacketBytes {
		return nil, mpierr.New(mpierr.CatLimit, rd.tracker.LastExt()+1, "",
			fmt.Sprintf("packet length %d outside [1,%d]", pktLen, MaxPacketBytes)).
			AtOffset(pktStart)
	}
	if rd.count >= MaxPackets {
		return nil, mpierr.New(mpierr.CatLimit, rd.tracker.LastExt()+1, "",
			fmt.Sprintf("packet count exceeds %d", MaxPackets)).AtOffset(pktStart)
	}
	buf := make([]byte, pktLen)
	if _, err := io.ReadFull(rd.r, buf); err != nil {
		return nil, mpierr.New(mpierr.CatTruncated, rd.tracker.LastExt()+1, "",
			"stream ended inside RTP packet").AtOffset(rd.streamPos).Wrap(err)
	}
	rd.streamPos += int64(pktLen)
	rd.count++

	pkt := &rtp.Packet{}
	if err := pkt.Unmarshal(buf); err != nil {
		return nil, mpierr.New(mpierr.CatCorrupt, rd.tracker.LastExt()+1, "",
			"RTP header unmarshal failed").AtOffset(pktStart).Wrap(err)
	}
	tr := rd.tracker.Push(pkt.SequenceNumber)
	if tr.Duplicate {
		rd.duplicates++
	}
	frame, err := parseFrame(pkt.Payload)
	if err != nil {
		if me, ok := err.(*mpierr.MediaError); ok {
			me.FirstSeq = tr.Seq
			// Full stream offset: length prefix + packet start + RTP header
			// + offset inside the frame payload.
			me.ByteOffset += pktStart + 2 + int64(len(buf)-len(pkt.Payload))
			return nil, me
		}
		return nil, err
	}
	frame.ExtSeq = tr.Seq
	frame.Duplicate = tr.Duplicate
	frame.Seq16 = pkt.SequenceNumber
	frame.Timestamp = pkt.Timestamp
	frame.SSRC = pkt.SSRC
	frame.SampleRate = rd.sampleRate
	frame.StreamOffset = pktStart
	return frame, err
}

// parseFrame validates the frame header and per-block checksums.
func parseFrame(payload []byte) (*Frame, error) {
	if len(payload) < frameHeaderSize {
		return nil, mpierr.New(mpierr.CatTruncated, 0, "",
			fmt.Sprintf("frame header needs %d bytes, got %d", frameHeaderSize, len(payload))).
			AtOffset(0)
	}
	if payload[0] != frameMagic0 || payload[1] != frameMagic1 {
		return nil, mpierr.New(mpierr.CatCorrupt, 0, "", "bad frame magic").AtOffset(0)
	}
	samples := binary.BigEndian.Uint16(payload[4:6])
	blockSize := binary.BigEndian.Uint16(payload[6:8])
	blocks := binary.BigEndian.Uint16(payload[8:10])
	if blockSize == 0 || blocks == 0 {
		return nil, mpierr.New(mpierr.CatCorrupt, 0, "", "frame block size/count is zero").AtOffset(6)
	}
	dataEnd := frameHeaderSize + int(blockSize)*int(blocks)
	if dataEnd > MaxFramePayload || dataEnd+int(blocks)*blockCheckSize != len(payload) {
		return nil, mpierr.New(mpierr.CatTruncated, 0, "",
			fmt.Sprintf("frame length %d does not match block table", len(payload))).
			AtOffset(int64(frameHeaderSize))
	}
	data := payload[frameHeaderSize:dataEnd]
	table := payload[dataEnd:]
	for b := 0; b < int(blocks); b++ {
		lo := b * int(blockSize)
		want := binary.BigEndian.Uint32(table[b*blockCheckSize:])
		if crc32.ChecksumIEEE(data[lo:lo+int(blockSize)]) != want {
			// Offset is relative to the frame payload; Reader adds the
			// packet/stream base. One block == one byte by default.
			return nil, mpierr.New(mpierr.CatCorrupt, 0, "",
				fmt.Sprintf("block %d checksum mismatch", b)).AtOffset(int64(frameHeaderSize + lo))
		}
	}
	return &Frame{
		Samples:     samples,
		Payload:     append([]byte(nil), data...),
		FrameOffset: int64(frameHeaderSize),
		BlockSize:   blockSize,
	}, nil
}
