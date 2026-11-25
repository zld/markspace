package kernel

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

var wireMagic = [4]byte{'R', 'P', 'K', '1'}

const (
	wireTypeSample uint8 = 1
	wireTypeEvent  uint8 = 2
)

// WireMessage is the compact private payload carried by fixture TS PIDs.
type WireMessage struct {
	IsEvent     bool
	TrackKind   TrackKind
	PID         uint16
	Index       uint32
	PTS90k      uint64
	Duration90k uint32
	Flags       uint16
	Label       string
	Data        []byte
}

// EncodeMessage produces a deterministic length-prefixed payload.
func EncodeMessage(m WireMessage) ([]byte, error) {
	if len(m.Label) > 255 || len(m.Data) > 65535 {
		return nil, fmt.Errorf("message field too large")
	}
	var b bytes.Buffer
	b.Write(wireMagic[:])
	msgType := wireTypeSample
	if m.IsEvent {
		msgType = wireTypeEvent
	}
	if err := b.WriteByte(msgType); err != nil {
		return nil, err
	}
	kind := byte('d')
	switch m.TrackKind {
	case TrackAudio:
		kind = 'a'
	case TrackVideo:
		kind = 'v'
	}
	b.WriteByte(kind)
	_ = binary.Write(&b, binary.BigEndian, m.PID)
	_ = binary.Write(&b, binary.BigEndian, m.Index)
	_ = binary.Write(&b, binary.BigEndian, m.PTS90k)
	_ = binary.Write(&b, binary.BigEndian, m.Duration90k)
	_ = binary.Write(&b, binary.BigEndian, m.Flags)
	_ = b.WriteByte(byte(len(m.Label)))
	_, _ = b.WriteString(m.Label)
	_ = binary.Write(&b, binary.BigEndian, uint16(len(m.Data)))
	_, _ = b.Write(m.Data)
	return b.Bytes(), nil
}

// DecodeMessage parses one fixture payload.
func DecodeMessage(payload []byte) (WireMessage, error) {
	r := bytes.NewReader(payload)
	var magic [4]byte
	if _, err := io.ReadFull(r, magic[:]); err != nil || magic != wireMagic {
		return WireMessage{}, fmt.Errorf("bad message magic")
	}
	var typ, kindByte byte
	if err := binary.Read(r, binary.BigEndian, &typ); err != nil {
		return WireMessage{}, err
	}
	if err := binary.Read(r, binary.BigEndian, &kindByte); err != nil {
		return WireMessage{}, err
	}
	m := WireMessage{IsEvent: typ == wireTypeEvent}
	switch kindByte {
	case 'a':
		m.TrackKind = TrackAudio
	case 'v':
		m.TrackKind = TrackVideo
	default:
		m.TrackKind = TrackData
	}
	if err := binary.Read(r, binary.BigEndian, &m.PID); err != nil {
		return m, err
	}
	if err := binary.Read(r, binary.BigEndian, &m.Index); err != nil {
		return m, err
	}
	if err := binary.Read(r, binary.BigEndian, &m.PTS90k); err != nil {
		return m, err
	}
	if err := binary.Read(r, binary.BigEndian, &m.Duration90k); err != nil {
		return m, err
	}
	if err := binary.Read(r, binary.BigEndian, &m.Flags); err != nil {
		return m, err
	}
	var labelLen uint8
	if err := binary.Read(r, binary.BigEndian, &labelLen); err != nil {
		return m, err
	}
	label := make([]byte, labelLen)
	if _, err := io.ReadFull(r, label); err != nil {
		return m, err
	}
	m.Label = string(label)
	var dataLen uint16
	if err := binary.Read(r, binary.BigEndian, &dataLen); err != nil {
		return m, err
	}
	m.Data = make([]byte, dataLen)
	if _, err := io.ReadFull(r, m.Data); err != nil {
		return m, err
	}
	return m, nil
}
