package rtp

import (
	"encoding/binary"
	"hash/crc32"
	"io"

	"github.com/pion/rtp"
)

// FrameVersion is the fixture frame format version emitted by builders.
const FrameVersion byte = 1

// FrameHeaderSize is the fixed fixture frame header length in bytes.
const FrameHeaderSize = frameHeaderSize

// BuildMediaFrame constructs a checksummed fixture media frame: header,
// sample data and one CRC32 per block. It is the encoder counterpart of the
// parser in packet.go, shared by fixture generation and tests.
func BuildMediaFrame(flags byte, samples, blockSize uint16, data []byte) []byte {
	if blockSize == 0 || len(data)%int(blockSize) != 0 {
		panic("rtp: invalid frame block size or data length")
	}
	blocks := len(data) / int(blockSize)
	out := make([]byte, 0, frameHeaderSize+len(data)+blocks*blockCheckSize)
	out = append(out, frameMagic0, frameMagic1, FrameVersion, flags)
	out = binary.BigEndian.AppendUint16(out, samples)
	out = binary.BigEndian.AppendUint16(out, blockSize)
	out = binary.BigEndian.AppendUint16(out, uint16(blocks))
	out = append(out, data...)
	for b := 0; b < blocks; b++ {
		lo := b * int(blockSize)
		out = binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(data[lo:lo+int(blockSize)]))
	}
	return out
}

// WriteStreamPacket writes one RTP packet with its 2-byte big-endian length
// prefix, matching Reader's wire format.
func WriteStreamPacket(w io.Writer, pkt *rtp.Packet) error {
	raw, err := pkt.Marshal()
	if err != nil {
		return err
	}
	var lenBuf [2]byte
	binary.BigEndian.PutUint16(lenBuf[:], uint16(len(raw)))
	if _, err := w.Write(lenBuf[:]); err != nil {
		return err
	}
	_, err = w.Write(raw)
	return err
}
