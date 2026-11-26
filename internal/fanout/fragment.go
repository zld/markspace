package fanout

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"mediapack/internal/mp4idx"
	"mediapack/internal/rtpcheck"
)

// FragmentMagic identifies a mediapack fragment container.
const FragmentMagic = "MPCKFRG1" // exactly 8 bytes

// Header is the JSON fragment header.
type Header struct {
	Magic            string `json:"magic"`
	ID               string `json:"id"`
	IntervalIndex    int    `json:"interval_index"`
	RateIndex        int    `json:"rate_index"`
	RateName         string `json:"rate_name"`
	SourceStart      int64  `json:"source_start"`
	SourceEnd        int64  `json:"source_end"`
	StartTick        uint64 `json:"start_tick"`
	EndTick          uint64 `json:"end_tick"`
	StructuralDigest string `json:"structural_digest"`
}

// BuildInput carries everything needed to render one representation fragment.
type BuildInput struct {
	Header      Header
	Keyframes   []int64
	Source      io.ReadSeeker
	SourceStart int64
	SourceEnd   int64
	RTPSSRC     uint32
	EnableRTP   bool
}

// BuildFragment reads the source interval and assembles the deterministic
// fragment container: magic | header | MP4 index box | raw TS range.
func BuildFragment(in BuildInput) ([]byte, error) {
	if in.SourceEnd <= in.SourceStart {
		return nil, fmt.Errorf("invalid interval [%d,%d)", in.SourceStart, in.SourceEnd)
	}
	tsLen := in.SourceEnd - in.SourceStart
	if tsLen%tsPacketSize != 0 {
		return nil, fmt.Errorf("interval length %d not TS-188 aligned", tsLen)
	}
	tsBytes := make([]byte, tsLen)
	if _, err := in.Source.Seek(in.SourceStart, io.SeekStart); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(in.Source, tsBytes); err != nil {
		return nil, err
	}

	in.Header.Magic = FragmentMagic
	hb, err := json.Marshal(in.Header)
	if err != nil {
		return nil, err
	}
	mp4Bytes, err := mp4idx.Build(mp4idx.IndexPayload{
		IntervalID:  in.Header.ID,
		StartTick:   in.Header.StartTick,
		EndTick:     in.Header.EndTick,
		KeyframeOff: in.Keyframes,
	})
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	buf.WriteString(FragmentMagic)
	if err := writeU32(&buf, uint32(len(hb))); err != nil {
		return nil, err
	}
	buf.Write(hb)
	if err := writeU32(&buf, uint32(len(mp4Bytes))); err != nil {
		return nil, err
	}
	buf.Write(mp4Bytes)
	buf.Write(tsBytes)
	return buf.Bytes(), nil
}

func writeU32(buf *bytes.Buffer, v uint32) error {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	_, err := buf.Write(b[:])
	return err
}

// ParsedFragment is the decoded container.
type ParsedFragment struct {
	Header Header
	MP4    *mp4idx.IndexPayload
	TS     []byte
}

// ParseFragment decodes and validates container framing.
func ParseFragment(data []byte) (*ParsedFragment, error) {
	if len(data) < 16 || string(data[:8]) != FragmentMagic {
		return nil, errors.New("bad fragment magic or truncated header")
	}
	pos := 8
	hl := binary.BigEndian.Uint32(data[pos : pos+4])
	pos += 4
	if uint32(pos)+hl+4 > uint32(len(data)) {
		return nil, errors.New("header length out of range")
	}
	var h Header
	if err := json.Unmarshal(data[pos:pos+int(hl)], &h); err != nil {
		return nil, fmt.Errorf("header json: %w", err)
	}
	pos += int(hl)
	ml := binary.BigEndian.Uint32(data[pos : pos+4])
	pos += 4
	if uint32(pos)+ml > uint32(len(data)) {
		return nil, errors.New("mp4 index length out of range")
	}
	mp4Payload, _, err := mp4idx.Parse(data[pos : pos+int(ml)])
	if err != nil {
		return nil, fmt.Errorf("mp4 index parse: %w", err)
	}
	pos += int(ml)
	return &ParsedFragment{Header: h, MP4: mp4Payload, TS: data[pos:]}, nil
}

// CheckResult is the auxiliary structural validation outcome.
type CheckResult struct {
	RTP       rtpcheck.Result
	TSAligned bool
	MP4OK     bool
}

// Check parses the fragment and runs MP4-index and RTP structural checks.
func Check(data []byte) (*CheckResult, error) {
	pf, err := ParseFragment(data)
	if err != nil {
		return nil, err
	}
	res := &CheckResult{TSAligned: len(pf.TS)%tsPacketSize == 0, MP4OK: pf.MP4 != nil}
	if !res.TSAligned {
		return res, errors.New("fragment TS payload not 188 aligned")
	}
	rtpRes, err := rtpcheck.Validate(pf.TS, 0x4D50434B, uint16(pf.Header.IntervalIndex)*7,
		uint32(pf.Header.StartTick))
	if err != nil {
		return res, err
	}
	res.RTP = rtpRes
	return res, nil
}

const tsPacketSize = 188
