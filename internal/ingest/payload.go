package ingest

import (
	"encoding/binary"
	"hash/crc32"

	"medpipe/internal/clock"
	"medpipe/internal/media"
)

const (
	PayloadMagic   = "MDPF"
	PayloadVersion = byte(1)
)

// FramePayload is the small deterministic media payload carried inside RTP.
// It includes a CRC so one corrupted byte is classified instead of causing
// unstable segmentation or fingerprint decisions.
type FramePayload struct {
	Flags           byte
	ContentType     byte
	FrameIndex      uint32
	DurationSamples uint32
	Energy          int32
	PatternID       uint32
	Content         []byte
}

const payloadFixedHeader = 26
const payloadCRCTrailer = 4

func EncodePayload(p FramePayload) []byte {
	if p.DurationSamples == 0 {
		p.DurationSamples = 960 // 20 ms at 48 kHz
	}
	out := make([]byte, payloadFixedHeader+len(p.Content)+payloadCRCTrailer)
	copy(out[0:4], PayloadMagic)
	out[4] = PayloadVersion
	out[5] = p.Flags
	out[6] = p.ContentType
	out[7] = 0
	binary.BigEndian.PutUint32(out[8:12], p.FrameIndex)
	binary.BigEndian.PutUint32(out[12:16], p.DurationSamples)
	binary.BigEndian.PutUint32(out[16:20], uint32(p.Energy))
	binary.BigEndian.PutUint32(out[20:24], p.PatternID)
	binary.BigEndian.PutUint16(out[24:26], uint16(len(p.Content)))
	copy(out[26:26+len(p.Content)], p.Content)
	crcAt := 26 + len(p.Content)
	crc := crc32.ChecksumIEEE(out[:crcAt])
	binary.BigEndian.PutUint32(out[crcAt:crcAt+4], crc)
	return out
}

func DecodePayload(raw []byte, loc *clock.Location, sampleRate uint32, point clock.Point) (*media.Frame, *clock.Error) {
	if len(raw) < payloadFixedHeader+payloadCRCTrailer {
		return nil, clock.Errorf(clock.CodeInvalidPacket, loc,
			"payload too short: %d bytes", len(raw))
	}
	if string(raw[0:4]) != PayloadMagic {
		return nil, clock.Errorf(clock.CodeMissingField, loc, "bad payload magic")
	}
	if raw[4] != PayloadVersion {
		return nil, clock.Errorf(clock.CodeUnsupportedPayload, loc, "unsupported payload version %d", raw[4])
	}
	contentLen := int(binary.BigEndian.Uint16(raw[24:26]))
	expectedLen := payloadFixedHeader + contentLen + payloadCRCTrailer
	if contentLen > len(raw)-payloadFixedHeader-payloadCRCTrailer || len(raw) != expectedLen {
		return nil, clock.Errorf(clock.CodeInvalidPacket, loc,
			"illegal payload length: declared_content=%d actual=%d", contentLen, len(raw))
	}
	crcAt := payloadFixedHeader + contentLen
	wantCRC := binary.BigEndian.Uint32(raw[crcAt : crcAt+4])
	gotCRC := crc32.ChecksumIEEE(raw[:crcAt])
	if wantCRC != gotCRC {
		return nil, clock.Errorf(clock.CodeCorruptPayload, loc,
			"payload CRC mismatch: want=%08x got=%08x", wantCRC, gotCRC)
	}

	p := FramePayload{
		Flags: raw[5], ContentType: raw[6],
		FrameIndex:      binary.BigEndian.Uint32(raw[8:12]),
		DurationSamples: binary.BigEndian.Uint32(raw[12:16]),
		Energy:          int32(binary.BigEndian.Uint32(raw[16:20])),
		PatternID:       binary.BigEndian.Uint32(raw[20:24]),
		Content:         append([]byte(nil), raw[26:crcAt]...),
	}
	if p.DurationSamples == 0 || sampleRate == 0 {
		return nil, clock.Errorf(clock.CodeMissingField, loc, "frame duration/sample rate missing")
	}
	duration := timeDurationFromSamples(p.DurationSamples, sampleRate)
	f := media.Frame{
		SSRC: point.SSRC, Sequence: point.Sequence, ExtSequence: point.ExtSequence,
		SourceOffset: loc.ByteOffset, PTS: point.MediaTime, Duration: duration,
		Key: p.Flags&0x01 != 0, Energy: int(p.Energy), ContentType: p.ContentType,
		Content: p.Content,
	}
	f.Hash = media.HashFrame(f)
	return &f, nil
}

func timeDurationFromSamples(samples uint32, rate uint32) (d timeDuration) {
	return timeDuration(uint64(samples) * uint64(clock.Second) / uint64(rate))
}

// timeDuration is an alias to avoid importing time only for one expression in
// this constrained codec file; it is identical to time.Duration.
type timeDuration = clock.TimeBase
