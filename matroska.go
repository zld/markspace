package mediakernel

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

// DefaultMaxBlock bounds a single block payload.
const DefaultMaxBlock = 4 << 20

// Block is one demuxed Matroska block with track, tick timestamp and offset.
type Block struct {
	Track  uint64
	Ticks  int64 // absolute timecode in segment ticks
	Offset int64 // byte offset of the block element header
	Size   int64
	Data   []byte // block payload after the block header (e.g. one RTP packet)
}

// Media is the parsed result: timescale, tracks and blocks in file order.
type Media struct {
	TimecodeScale uint64
	Registry      *TrackRegistry
	Blocks        []Block
}

// Parse walks a Matroska/WebM stream and extracts tracks and blocks.
func Parse(r io.Reader, total int64, maxBlock int64) (*Media, error) {
	return ParseStream(nil, r, total, maxBlock, func(m *Media, cluster []Block) error {
		m.Blocks = append(m.Blocks, cluster...)
		return nil
	})
}

// ParseStream walks a Matroska/WebM stream, invoking emit once per completed
// cluster with that cluster's blocks. Blocks are not accumulated across
// clusters, keeping memory bounded. ctx may be nil.
func ParseStream(ctx context.Context, r io.Reader, total int64, maxBlock int64, emit func(m *Media, cluster []Block) error) (*Media, error) {
	p := NewParser(r, total)
	m := &Media{TimecodeScale: 1_000_000, Registry: NewTrackRegistry()}
	var cur *Track
	var clusterTime uint64
	var clusterSet bool
	var clusterBlocks []Block
	flush := func() error {
		if len(clusterBlocks) == 0 {
			return nil
		}
		err := emit(m, clusterBlocks)
		clusterBlocks = nil
		return err
	}
	for {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
		}
		el, err := p.Next()
		if err == io.EOF {
			if err := flush(); err != nil {
				return nil, err
			}
			return m, nil
		}
		if err != nil {
			return nil, err
		}
		switch el.ID {
		case IDTimecodeScale:
			v, err := readUintEl(p, el)
			if err != nil {
				return nil, err
			}
			m.TimecodeScale = v
		case IDTrackEntry:
			cur = nil
		case IDTrackNumber:
			v, err := readUintEl(p, el)
			if err != nil {
				return nil, err
			}
			cur = m.Registry.GetOrCreate(v)
		case IDTrackType:
			v, err := readUintEl(p, el)
			if err != nil {
				return nil, err
			}
			if cur != nil {
				cur.Type = v
			}
		case IDCodecID:
			data, err := p.ReadData(el, 256)
			if err != nil {
				return nil, err
			}
			if cur != nil {
				cur.Codec = string(data)
			}
		case IDDefaultDuration:
			v, err := readUintEl(p, el)
			if err != nil {
				return nil, err
			}
			if cur != nil {
				cur.DefaultDuration = int64(v)
			}
		case IDTrackTimecodeScale:
			data, err := p.ReadData(el, 8)
			if err != nil {
				return nil, err
			}
			if cur != nil {
				switch len(data) {
				case 4:
					cur.TimecodeScale = float64(math.Float32frombits(binary.BigEndian.Uint32(data)))
				case 8:
					cur.TimecodeScale = math.Float64frombits(binary.BigEndian.Uint64(data))
				}
			}
		case IDCluster:
			if err := flush(); err != nil {
				return nil, err
			}
			clusterSet = false
		case IDTimecode:
			v, err := readUintEl(p, el)
			if err != nil {
				return nil, err
			}
			clusterTime, clusterSet = v, true
		case IDSimpleBlock, IDBlock:
			data, err := p.ReadData(el, maxBlock)
			if err != nil {
				return nil, err
			}
			blk, err := parseBlock(el, data, clusterTime, clusterSet)
			if err != nil {
				return nil, err
			}
			clusterBlocks = append(clusterBlocks, blk)
		default:
			if !el.Master {
				if err := p.Skip(el); err != nil {
					return nil, err
				}
			}
		}
	}
}

func readUintEl(p *Parser, el *Element) (uint64, error) {
	data, err := p.ReadData(el, 8)
	if err != nil {
		return 0, err
	}
	var v uint64
	for _, b := range data {
		v = v<<8 | uint64(b)
	}
	return v, nil
}

// parseBlock decodes a SimpleBlock/Block body: track VINT, int16 relative
// timecode, flags, then payload. Lacing is rejected explicitly.
func parseBlock(el *Element, data []byte, clusterTime uint64, clusterSet bool) (Block, error) {
	if len(data) < 1 {
		return Block{}, &Error{ErrTruncatedHeader, el.DataOffset, el.Path, "empty block"}
	}
	b0 := data[0]
	if b0 == 0 {
		return Block{}, &Error{ErrInvalidVINT, el.DataOffset, el.Path, "block track number VINT is zero"}
	}
	w := 1
	for mask := byte(0x80); w < 8 && b0&mask == 0; mask >>= 1 {
		w++
	}
	if len(data) < w+3 {
		return Block{}, &Error{ErrTruncatedHeader, el.DataOffset + int64(len(data)), el.Path,
			fmt.Sprintf("block header needs %d bytes, have %d", w+3, len(data))}
	}
	track := uint64(b0 & (0xFF >> w))
	for i := 1; i < w; i++ {
		track = track<<8 | uint64(data[i])
	}
	rel := int64(int16(binary.BigEndian.Uint16(data[w:])))
	flags := data[w+2]
	if flags&0x06 != 0 {
		return Block{}, &Error{ErrLacingUnsupported, el.DataOffset + int64(w) + 2, el.Path,
			fmt.Sprintf("lacing flags 0x%02x not supported", flags)}
	}
	if !clusterSet {
		return Block{}, &Error{ErrTSMissing, el.HeaderOffset, el.Path,
			"block appears in a cluster without a Timecode element"}
	}
	return Block{
		Track:  track,
		Ticks:  int64(clusterTime) + rel,
		Offset: el.HeaderOffset,
		Size:   el.Size,
		Data:   data[w+3:],
	}, nil
}
