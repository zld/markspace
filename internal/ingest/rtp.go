package ingest

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/pion/rtp"

	"mediarc/internal/model"
)

// RTPContainerMagic identifies the deterministic length-prefixed RTP log.
const RTPContainerMagic = "MRR1"

// MaxRTPPacket bounds a supported RTP record.
const MaxRTPPacket = 64 << 10

// StoredPacket is an RTP packet plus its original container offset.
type StoredPacket struct {
	Offset       int64 `json:"offset"`
	PacketOffset int64 `json:"packet_offset"`
	Length       int   `json:"length"`
	Packet       rtp.Packet
}

// Sequence returns the RTP sequence number.
func (p StoredPacket) Sequence() uint16 { return p.Packet.SequenceNumber }

// Timestamp returns the RTP timestamp.
func (p StoredPacket) Timestamp() uint32 { return p.Packet.Timestamp }

// WriteRTPFile writes the project's length-prefixed RTP container. Each record
// is [4-byte big-endian length][RTP packet]. A leading MRR1 magic makes the
// format explicit.
func WriteRTPFile(path string, packets []*rtp.Packet) ([]int64, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.WriteString(RTPContainerMagic); err != nil {
		return nil, err
	}
	offsets := make([]int64, 0, len(packets))
	for _, pkt := range packets {
		off, err := f.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, err
		}
		offsets = append(offsets, off)
		raw, err := pkt.Marshal()
		if err != nil {
			return nil, err
		}
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(raw)))
		if _, err := f.Write(lenBuf[:]); err != nil {
			return nil, err
		}
		if _, err := f.Write(raw); err != nil {
			return nil, err
		}
	}
	return offsets, f.Sync()
}

// ReadRTPFile reads all RTP records while retaining original offsets.
func ReadRTPFile(path string, faults *model.Collector) ([]StoredPacket, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return nil, fmt.Errorf("read RTP container magic: %w", err)
	}
	if string(magic[:]) != RTPContainerMagic {
		return nil, fmt.Errorf("unsupported RTP container %q", string(magic[:]))
	}
	var out []StoredPacket
	for {
		off, err := f.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, err
		}
		var lenBuf [4]byte
		if _, err := io.ReadFull(f, lenBuf[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			if errors.Is(err, io.ErrUnexpectedEOF) {
				faults.Add(model.Fault{Kind: model.FaultTruncated, Offset: off, Message: "partial RTP length", Recoverable: true})
				return out, nil
			}
			return out, err
		}
		n := binary.BigEndian.Uint32(lenBuf[:])
		if n == 0 || n > MaxRTPPacket {
			faults.Add(model.Fault{Kind: model.FaultBadLength, Offset: off, Actual: fmt.Sprint(n), Message: "invalid RTP length"})
			return out, nil
		}
		raw := make([]byte, n)
		if _, err := io.ReadFull(f, raw); err != nil {
			faults.Add(model.Fault{Kind: model.FaultTruncated, Offset: off + 4, Expected: fmt.Sprint(n), Message: "short RTP packet", Recoverable: true})
			return out, nil
		}
		var pkt rtp.Packet
		if err := pkt.Unmarshal(raw); err != nil {
			faults.Add(model.Fault{Kind: model.FaultBadLength, Offset: off + 4, Message: "invalid RTP packet: " + err.Error()})
			continue
		}
		out = append(out, StoredPacket{Offset: off, PacketOffset: off + 4, Length: len(raw), Packet: pkt})
	}
}

// NewAudioRTPPacket constructs one deterministic L16 RTP packet.
func NewAudioRTPPacket(seq uint16, timestamp uint32, ssrc uint32, payload []byte) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: seq,
			Timestamp:      timestamp,
			SSRC:           ssrc,
		},
		Payload: append([]byte(nil), payload...),
	}
}
