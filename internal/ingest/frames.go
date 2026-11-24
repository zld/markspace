package ingest

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"

	"mediarc/internal/model"
)

// FrameMagic identifies the supported media-archive frame container.
const FrameMagic = "MRC1"

// FrameHeaderSize is the fixed envelope payload header.
const FrameHeaderSize = 78

// MaxFrameEnvelope bounds the supported synthetic frame record size.
const MaxFrameEnvelope = 64 << 20

// FrameMeta is validated frame metadata without retaining pixel payload.
type FrameMeta struct {
	Offset         int64               `json:"offset"`
	PayloadOffset  int64               `json:"payload_offset"`
	MaterialID     string              `json:"material_id"`
	RecordIndex    uint32              `json:"record_index"`
	PTS            uint64              `json:"pts"`
	Duration       uint32              `json:"duration"`
	Width          uint16              `json:"width"`
	Height         uint16              `json:"height"`
	KeyFrame       bool                `json:"key_frame"`
	PayloadLength  uint32              `json:"payload_length"`
	EnvelopeLength uint32              `json:"envelope_length"`
	SHA256         [32]byte            `json:"sha256"`
	Color          model.ColorMetadata `json:"color"`
	ChecksumOK     bool                `json:"checksum_ok"`
	LengthOK       bool                `json:"length_ok"`
	TimestampOK    bool                `json:"timestamp_ok"`
	Valid          bool                `json:"valid"`
}

// FrameRecord is a fully read frame including its payload.
type FrameRecord struct {
	FrameMeta
	Payload []byte `json:"-"`
}

// ScanFrameFile validates framing, timestamp grid, declared/actual length and
// SHA-256. It retains offsets but not payload bytes, making a scan safe for
// large archives. ReadFramePayload can later fetch one winner per time slot.
func ScanFrameFile(path string, faults *model.Collector) ([]FrameMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var metas []FrameMeta
	for {
		off, err := f.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, err
		}
		var outer [8]byte
		if _, readErr := io.ReadFull(f, outer[:]); readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return metas, nil
			}
			if errors.Is(readErr, io.ErrUnexpectedEOF) {
				faults.Add(model.Fault{Kind: model.FaultTruncated, Offset: off, RecordStart: off,
					RecordIndex: int64(len(metas)), Message: "partial frame outer header", Recoverable: true})
				return metas, nil
			}
			return metas, readErr
		}
		if string(outer[:4]) != FrameMagic {
			faults.Add(model.Fault{Kind: model.FaultBadMagic, Offset: off, RecordStart: off,
				Message: "bad frame magic", Recoverable: false})
			if _, err := f.Seek(off+1, io.SeekStart); err != nil {
				return metas, err
			}
			continue
		}
		envLen := binary.BigEndian.Uint32(outer[4:8])
		if envLen < FrameHeaderSize || envLen > MaxFrameEnvelope {
			faults.Add(model.Fault{Kind: model.FaultBadLength, Offset: off + 4, RecordStart: off,
				RecordIndex: int64(len(metas)), Actual: fmt.Sprint(envLen), Message: "invalid envelope length"})
			return metas, nil
		}
		env := make([]byte, envLen)
		if _, err := io.ReadFull(f, env); err != nil {
			faults.Add(model.Fault{Kind: model.FaultTruncated, Offset: off + 8, RecordStart: off,
				RecordIndex: int64(len(metas)), Expected: fmt.Sprint(envLen), Message: "short frame envelope",
				Recoverable: true})
			return metas, nil
		}
		meta, fault := DecodeFrameEnvelope(env, off)
		if fault != nil {
			faults.Add(*fault)
		}
		metas = append(metas, meta)
	}
}

// DecodeFrameEnvelope parses and validates one envelope. The envelope begins
// at the fixed frame header; outer offset is supplied for diagnostics.
func DecodeFrameEnvelope(env []byte, outerOffset int64) (FrameMeta, *model.Fault) {
	meta := FrameMeta{Offset: outerOffset, EnvelopeLength: uint32(len(env))}
	if len(env) < FrameHeaderSize {
		return meta, &model.Fault{Kind: model.FaultTruncated, Offset: outerOffset + 8 + int64(len(env)),
			RecordStart: outerOffset, Message: "envelope shorter than frame header", Recoverable: true}
	}
	var material [16]byte
	copy(material[:], env[0:16])
	meta.SetMaterialID(material[:])
	meta.RecordIndex = binary.BigEndian.Uint32(env[16:20])
	meta.PTS = binary.BigEndian.Uint64(env[20:28])
	meta.Duration = binary.BigEndian.Uint32(env[28:32])
	meta.Width = binary.BigEndian.Uint16(env[32:34])
	meta.Height = binary.BigEndian.Uint16(env[34:36])
	flags := binary.BigEndian.Uint16(env[36:38])
	meta.KeyFrame = flags&0x0001 != 0
	meta.PayloadLength = binary.BigEndian.Uint32(env[38:42])
	copy(meta.SHA256[:], env[42:74])
	meta.Color = decodeColor(env[74:78])
	meta.PayloadOffset = outerOffset + 8 + FrameHeaderSize

	actualPayload := len(env) - FrameHeaderSize
	meta.LengthOK = uint64(actualPayload) == uint64(meta.PayloadLength) && meta.PayloadLength > 0
	meta.TimestampOK = meta.Duration > 0 && meta.PTS == uint64(meta.RecordIndex)*uint64(meta.Duration)
	meta.ChecksumOK = false
	if meta.LengthOK {
		sum := sha256.Sum256(env[FrameHeaderSize:])
		meta.ChecksumOK = bytes.Equal(sum[:], meta.SHA256[:])
	}
	meta.Valid = meta.LengthOK && meta.TimestampOK && meta.ChecksumOK

	idx := int64(meta.RecordIndex)
	switch {
	case !meta.LengthOK && actualPayload < int(meta.PayloadLength):
		f := model.Fault{Kind: model.FaultTruncated, MaterialID: meta.MaterialID, RecordIndex: idx,
			Offset: meta.PayloadOffset + int64(actualPayload), RecordStart: outerOffset,
			Expected: fmt.Sprint(meta.PayloadLength), Actual: fmt.Sprint(actualPayload),
			Message: "frame payload is truncated", Recoverable: true}
		return meta, &f
	case !meta.LengthOK:
		f := model.Fault{Kind: model.FaultBadLength, MaterialID: meta.MaterialID, RecordIndex: idx,
			Offset: outerOffset + 8 + 38, RecordStart: outerOffset,
			Expected: fmt.Sprint(meta.PayloadLength), Actual: fmt.Sprint(actualPayload),
			Message: "frame payload length mismatch"}
		return meta, &f
	case !meta.TimestampOK:
		f := model.Fault{Kind: model.FaultBadTimestamp, MaterialID: meta.MaterialID, RecordIndex: idx,
			Offset: outerOffset + 8 + 20, RecordStart: outerOffset, Actual: fmt.Sprint(meta.PTS),
			Expected: fmt.Sprint(uint64(meta.RecordIndex) * uint64(meta.Duration)),
			Message:  "frame timestamp is off declared grid"}
		return meta, &f
	case !meta.ChecksumOK:
		f := model.Fault{Kind: model.FaultBadSHA, MaterialID: meta.MaterialID, RecordIndex: idx,
			Offset: meta.PayloadOffset, RecordStart: outerOffset, Message: "SHA-256 mismatch",
			Expected: fmt.Sprintf("%x", meta.SHA256), Recoverable: true}
		return meta, &f
	}
	return meta, nil
}

// SetMaterialID decodes a NUL-padded material identifier.
func (m *FrameMeta) SetMaterialID(b []byte) {
	m.MaterialID = string(bytes.TrimRight(b, "\x00"))
}

// ReadFramePayload reads exactly one validated payload by recorded offset.
func ReadFramePayload(path string, m FrameMeta) ([]byte, error) {
	if !m.LengthOK {
		return nil, fmt.Errorf("frame %d has invalid length", m.RecordIndex)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(m.PayloadOffset, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, m.PayloadLength)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(buf)
	if !bytes.Equal(sum[:], m.SHA256[:]) {
		return nil, fmt.Errorf("frame %d: payload changed since scan", m.RecordIndex)
	}
	return buf, nil
}
