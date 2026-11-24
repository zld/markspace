package container

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/local/medialab/internal/timeline"
)

// MXF local tags mirror the fixture's private KLV pack. They are kept in
// the parser so the parser never imports fixture internals.
const (
	mxfTagTrackID       = 0x10
	mxfTagEditRateNum   = 0x11
	mxfTagEditRateDen   = 0x12
	mxfTagDuration      = 0x13
	mxfTagOrigin        = 0x14
	mxfTagDisplayWidth  = 0x15
	mxfTagDisplayHeight = 0x16
	mxfTagColorPrim     = 0x17
	mxfTagRotation      = 0x18
	mxfTagIndexTable    = 0x19
	mxfTagEssenceStart  = 0x1A
)

// mxfPrefaceKey is the private UL the fixture uses for metadata packs.
var mxfPrefaceKey = [16]byte{0x06, 0x0E, 0x2B, 0x34, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02}

// MXFIndexEntry is one row of the recovered index table.
type MXFIndexEntry struct {
	FrameOffset uint32
	Keyframe    bool
}

// MXFMeta is the structured result of parsing one MXF sample.
type MXFMeta struct {
	FileMeta
	EditRateNum  int64
	EditRateDen  int64
	Duration     int64
	Origin       int64
	EssenceStart int64
	Index        []MXFIndexEntry
	width, height uint32
}

// ParseMXF walks the KLV stream using only the Go standard library and
// recovers the preface local items. Truncated KLV triplets produce an
// error carrying the absolute offset for review.
func ParseMXF(data []byte) (*MXFMeta, error) {
	meta := &MXFMeta{}
	meta.Format = "mxf"

	pos := 0
	for pos < len(data) {
		if len(data)-pos < 16 {
			return nil, fmt.Errorf("mxf: truncated KLV key at offset %d", pos)
		}
		var key [16]byte
		copy(key[:], data[pos:pos+16])
		length, consumed, ok := decodeBER(data[pos+16:])
		if !ok {
			return nil, fmt.Errorf("mxf: invalid BER length at offset %d", pos+16)
		}
		valueStart := pos + 16 + consumed
		if length < 0 || valueStart+length > len(data) {
			return nil, fmt.Errorf("mxf: KLV value extends past EOF at offset %d (declared %d bytes)",
				valueStart, length)
		}
		if key == mxfPrefaceKey {
			if err := parsePreface(data[valueStart:valueStart+length], meta); err != nil {
				return nil, fmt.Errorf("mxf: %w", err)
			}
		}
		pos = valueStart + length
	}

	if meta.EditRateNum <= 0 || meta.EditRateDen <= 0 {
		return nil, errors.New("mxf: missing edit rate in preface")
	}
	meta.buildTrack()
	return meta, nil
}

func (m *MXFMeta) buildTrack() {
	frameDuration := timeline.ToTicksRational(1, m.EditRateNum, m.EditRateDen)
	tr := TrackDesc{
		ID: 1, Kind: "video",
		TimescaleHz:   m.EditRateNum / m.EditRateDen,
		FrameRateNum:  m.EditRateNum,
		FrameRateDen:  m.EditRateDen,
		SampleCount:   int(m.Duration),
		DurationTicks: frameDuration * m.Duration,
		Width:         m.width,
		Height:        m.height,
		Rotation:      m.Rotation,
		Color:         m.Color,
	}
	for i, e := range m.Index {
		if e.Keyframe {
			tr.KeyframeSampleNumbers = append(tr.KeyframeSampleNumbers, uint32(i+1))
		}
	}
	m.Tracks = []TrackDesc{tr}
	m.HasIndex = len(m.Index) > 0
	m.FileMeta.Color = m.Color
	m.FileMeta.Rotation = m.Rotation
}

type localItem struct {
	tag   byte
	value []byte
}

func parsePreface(b []byte, m *MXFMeta) error {
	items, err := scanLocalItems(b)
	if err != nil {
		return err
	}
	for _, it := range items {
		switch it.tag {
		case mxfTagEditRateNum:
			m.EditRateNum = int64(binary.BigEndian.Uint64(it.value))
		case mxfTagEditRateDen:
			m.EditRateDen = int64(binary.BigEndian.Uint64(it.value))
		case mxfTagDuration:
			m.Duration = int64(binary.BigEndian.Uint64(it.value))
		case mxfTagOrigin:
			m.Origin = int64(binary.BigEndian.Uint64(it.value))
		case mxfTagDisplayWidth:
			m.width = binary.BigEndian.Uint32(it.value)
		case mxfTagDisplayHeight:
			m.height = binary.BigEndian.Uint32(it.value)
		case mxfTagColorPrim:
			if m.Color == nil {
				m.Color = &ColorDesc{Source: "mxf"}
			}
			m.Color.Primaries = binary.BigEndian.Uint16(it.value)
		case mxfTagRotation:
			m.Rotation = int(int32(binary.BigEndian.Uint32(it.value)))
		case mxfTagEssenceStart:
			m.EssenceStart = int64(binary.BigEndian.Uint64(it.value))
		case mxfTagIndexTable:
			m.Index = parseIndex(it.value)
		}
	}
	return nil
}

func parseIndex(b []byte) []MXFIndexEntry {
	var out []MXFIndexEntry
	for len(b) >= 8 {
		out = append(out, MXFIndexEntry{
			FrameOffset: binary.BigEndian.Uint32(b[:4]),
			Keyframe:    binary.BigEndian.Uint32(b[4:8]) == 1,
		})
		b = b[8:]
	}
	return out
}

func scanLocalItems(b []byte) ([]localItem, error) {
	var out []localItem
	p := 0
	for p < len(b) {
		if p+1 >= len(b) {
			return nil, fmt.Errorf("truncated local item header at %d", p)
		}
		tag := b[p]
		n, consumed, ok := decodeBER(b[p+1:])
		if !ok {
			return nil, fmt.Errorf("invalid local item BER at %d", p+1)
		}
		start := p + 1 + consumed
		if start+n > len(b) {
			return nil, fmt.Errorf("local item 0x%02x extends past pack end", tag)
		}
		out = append(out, localItem{tag: tag, value: b[start : start+n]})
		p = start + n
	}
	return out, nil
}

// decodeBER parses a SMPTE 336M BER length. Short form uses one byte;
// long form prefixes the byte count in the low 7 bits.
func decodeBER(b []byte) (value, consumed int, ok bool) {
	if len(b) == 0 {
		return 0, 0, false
	}
	if b[0]&0x80 == 0 {
		return int(b[0]), 1, true
	}
	n := int(b[0] & 0x7F)
	if n == 0 || n > 8 || 1+n > len(b) {
		return 0, 0, false
	}
	v := 0
	for i := 1; i <= n; i++ {
		v = v<<8 | int(b[i])
	}
	return v, 1 + n, true
}
