package stream

import (
	"encoding/binary"
	"hash/crc32"
	"io"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

// StreamMagic identifies a MediaPipe packet stream.
var StreamMagic = [8]byte{'M', 'P', 'I', 'P', 'E', 'S', 'T', '1'}

const (
	FileVersion      uint16 = 1
	FileHeaderSize   uint16 = 24
	RecordHeaderSize        = 20

	RecordRTP  uint8 = 1
	RecordRTCP uint8 = 2

	FlagCorrupt uint8 = 1 << 0
)

// FileHeader is the fixed, byte-exact stream header.
type FileHeader struct {
	Magic      [8]byte
	Version    uint16
	HeaderSize uint16
	ClockRate  uint32
	Flags      uint32
}

func (h FileHeader) Marshal() []byte {
	b := make([]byte, FileHeaderSize)
	copy(b[0:8], h.Magic[:])
	binary.LittleEndian.PutUint16(b[8:10], h.Version)
	binary.LittleEndian.PutUint16(b[10:12], h.HeaderSize)
	binary.LittleEndian.PutUint32(b[12:16], h.ClockRate)
	binary.LittleEndian.PutUint32(b[16:20], h.Flags)
	return b
}

// RecordHeader precedes every embedded packet. PacketOffset is the generator's
// monotonic source position and is safe to surface in logs and reports.
type RecordHeader struct {
	Type         uint8
	Flags        uint8
	Reserved     uint16
	PayloadLen   uint32
	CRC32        uint32
	PacketOffset uint64
}

func (h RecordHeader) Marshal() []byte {
	b := make([]byte, RecordHeaderSize)
	b[0] = h.Type
	b[1] = h.Flags
	binary.LittleEndian.PutUint16(b[2:4], h.Reserved)
	binary.LittleEndian.PutUint32(b[4:8], h.PayloadLen)
	binary.LittleEndian.PutUint32(b[8:12], h.CRC32)
	binary.LittleEndian.PutUint64(b[12:20], h.PacketOffset)
	return b
}

// Writer writes framed RTP/RTCP records.
type Writer struct {
	w            io.Writer
	packetOffset uint64
}

func NewWriter(w io.Writer, clockRate uint32) (*Writer, error) {
	h := FileHeader{Magic: StreamMagic, Version: FileVersion, HeaderSize: FileHeaderSize, ClockRate: clockRate}
	if _, err := w.Write(h.Marshal()); err != nil {
		return nil, err
	}
	return &Writer{w: w}, nil
}

func (w *Writer) WriteRTP(p *rtp.Packet) (int64, error) {
	raw, err := p.Marshal()
	if err != nil {
		return 0, err
	}
	return w.WriteRaw(RecordRTP, raw)
}

func (w *Writer) WriteRTCP(packets ...rtcp.Packet) (int64, error) {
	raw, err := rtcp.Marshal(packets)
	if err != nil {
		return 0, err
	}
	return w.WriteRaw(RecordRTCP, raw)
}

func (w *Writer) WriteRaw(kind uint8, payload []byte) (int64, error) {
	offset := w.packetOffset
	h := RecordHeader{
		Type:         kind,
		PayloadLen:   uint32(len(payload)),
		CRC32:        crc32.ChecksumIEEE(payload),
		PacketOffset: offset,
	}
	if _, err := w.w.Write(h.Marshal()); err != nil {
		return int64(offset), err
	}
	if _, err := w.w.Write(payload); err != nil {
		return int64(offset), err
	}
	w.packetOffset++
	return int64(offset), nil
}
