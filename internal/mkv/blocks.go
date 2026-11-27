// Package mkv reconstructs Matroska/WebM tracks and interleaved blocks from
// EBML elements. Blocks keep their source byte offsets and original raw
// timecodes; timestamp regressions and duplicates are recorded as anomalies
// rather than silently corrected.
package mkv

import (
	"io"
	"sort"

	"github.com/local/tracecheck/internal/clock"
	"github.com/local/tracecheck/internal/ebml"
)

// DefaultTimecodeScale is the Matroska default: one timecode tick equals
// one millisecond, expressed in nanoseconds.
const DefaultTimecodeScale uint64 = 1_000_000

// Track describes one declared Matroska track.
type Track struct {
	Number  uint64
	UID     uint64
	CodecID string
}

// Block is one SimpleBlock/Block reconstructed from the stream.
type Block struct {
	TrackNumber  uint64
	RelTimecode  int16 // raw signed 16-bit offset from cluster timecode
	AbsTime      clock.Tick
	Flags        uint8
	SourceOffset int64 // offset of the block element header in the file
	Payload      []byte
}

// TimeAnomaly records an out-of-order or repeated timestamp without
// modifying the underlying block.
type TimeAnomaly struct {
	Code         clock.Code
	TrackNumber  uint64
	RawTimecode  int64 // original cluster-relative timecode value
	AbsTime      clock.Tick
	PrevTime     clock.Tick
	SourceOffset int64
}

// Document holds the reconstructed timeline.
type Document struct {
	TimecodeScale uint64
	Tracks        []Track
	Blocks        []*Block // source arrival order
	Anomalies     []TimeAnomaly
}

// SortedBlocks returns blocks ordered by (absolute time, track number,
// source offset) using a stable sort, so equal keys keep arrival order.
// The source-order slice is left untouched.
func (d *Document) SortedBlocks() []*Block {
	out := make([]*Block, len(d.Blocks))
	copy(out, d.Blocks)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.AbsTime != b.AbsTime {
			return a.AbsTime < b.AbsTime
		}
		if a.TrackNumber != b.TrackNumber {
			return a.TrackNumber < b.TrackNumber
		}
		return a.SourceOffset < b.SourceOffset
	})
	return out
}

type parseState struct {
	doc        *Document
	clusterTC  map[int64]int64  // cluster header offset -> cluster timecode
	trackEntry map[int64]*Track // track-entry header offset -> partial track
	lastTime   map[uint64]clock.Tick
}

// Parse reads a Matroska/WebM subset document from r. size is the total
// stream length in bytes.
func Parse(r io.ReadSeeker, size int64) (*Document, error) {
	rd := ebml.NewReader(r, size)
	st := &parseState{
		doc:        &Document{TimecodeScale: DefaultTimecodeScale},
		clusterTC:  map[int64]int64{},
		trackEntry: map[int64]*Track{},
		lastTime:   map[uint64]clock.Tick{},
	}
	err := rd.Walk(nil, func(el *ebml.Element, depth int) error {
		switch el.ID {
		case ebml.IDTimeScale:
			data, err := rd.ReadPayload(el)
			if err != nil {
				return err
			}
			v, err := ebml.Uint(data)
			if err != nil {
				return err
			}
			if v == 0 {
				return clock.NewError(clock.CodeOutOfRange, el.Offset, el.Path(), "TimecodeScale must be non-zero")
			}
			st.doc.TimecodeScale = v
		case ebml.IDTrackEntry:
			st.trackEntry[el.Offset] = &Track{}
		case ebml.IDTrackNumber, 0x73C5, 0x86:
			if el.Parent == nil || el.Parent.ID != ebml.IDTrackEntry {
				return nil
			}
			tr := st.trackEntry[el.Parent.Offset]
			if tr == nil {
				return nil
			}
			data, err := rd.ReadPayload(el)
			if err != nil {
				return err
			}
			switch el.ID {
			case ebml.IDTrackNumber:
				tr.Number, _ = ebml.Uint(data)
			case 0x73C5:
				tr.UID, _ = ebml.Uint(data)
			case 0x86:
				tr.CodecID = string(data)
			}
		case ebml.IDTimecode:
			if el.Parent != nil && el.Parent.ID == ebml.IDCluster {
				data, err := rd.ReadPayload(el)
				if err != nil {
					return err
				}
				v, err := ebml.Uint(data)
				if err != nil {
					return err
				}
				st.clusterTC[el.Parent.Offset] = int64(v)
			}
		case ebml.IDSimpleBlock, ebml.IDBlock:
			return st.readBlock(rd, el)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, tr := range st.trackEntry {
		st.doc.Tracks = append(st.doc.Tracks, *tr)
	}
	sort.Slice(st.doc.Tracks, func(i, j int) bool {
		return st.doc.Tracks[i].Number < st.doc.Tracks[j].Number
	})
	return st.doc, nil
}

func (st *parseState) readBlock(rd *ebml.Reader, el *ebml.Element) error {
	payload, err := rd.ReadPayload(el)
	if err != nil {
		return err
	}
	// Cross-check the element geometry through the go-mp4 BoxInfo adapter:
	// the bridged offset/header/end range must agree with the EBML reader,
	// so the offset every block reports is defensible against the MP4-style
	// structural model this project uses as its adaptation layer.
	bi := ebml.AsBoxInfo(el)
	if bi.Offset != uint64(el.Offset) || bi.HeaderSize != uint64(el.HeaderLen) {
		return clock.NewError(clock.CodeOutOfRange, el.Offset, el.Path(),
			"block geometry disagrees with structural adapter")
	}
	if el.Size >= 0 && el.EndOffset() != int64(bi.Offset+bi.Size) {
		return clock.NewError(clock.CodeOutOfRange, el.Offset, el.Path(),
			"block end offset disagrees with structural adapter")
	}
	trackNum, n, err := decodeLeadingVINT(payload)
	if err != nil {
		return clock.NewError(clock.CodeIllegalVINT, el.Offset, el.Path(),
			"block track number VINT invalid: "+err.Error())
	}
	if len(payload) < n+3 {
		return clock.NewError(clock.CodeTruncatedElement, el.Offset, el.Path(),
			"block header shorter than track/timecode/flags")
	}
	rel := int16(uint16(payload[n])<<8 | uint16(payload[n+1]))
	flags := payload[n+2]
	frame := payload[n+3:]

	clusterTC := int64(0)
	if el.Parent != nil {
		clusterTC = st.clusterTC[el.Parent.Offset]
	}
	rawTC := clusterTC + int64(rel)
	abs := clock.Tick(rawTC * int64(st.doc.TimecodeScale))

	blk := &Block{
		TrackNumber:  trackNum,
		RelTimecode:  rel,
		AbsTime:      abs,
		Flags:        flags,
		SourceOffset: el.Offset,
		Payload:      append([]byte(nil), frame...),
	}
	st.doc.Blocks = append(st.doc.Blocks, blk)

	if prev, ok := st.lastTime[trackNum]; ok {
		switch {
		case abs < prev:
			st.doc.Anomalies = append(st.doc.Anomalies, TimeAnomaly{
				Code: clock.CodeTimeRegression, TrackNumber: trackNum,
				RawTimecode: rawTC, AbsTime: abs, PrevTime: prev, SourceOffset: el.Offset,
			})
		case abs == prev:
			st.doc.Anomalies = append(st.doc.Anomalies, TimeAnomaly{
				Code: clock.CodeTimeDuplicate, TrackNumber: trackNum,
				RawTimecode: rawTC, AbsTime: abs, PrevTime: prev, SourceOffset: el.Offset,
			})
		}
	}
	st.lastTime[trackNum] = abs
	return nil
}

// decodeLeadingVINT decodes the leading VINT of a block payload, returning
// its numeric value and encoded width.
func decodeLeadingVINT(data []byte) (uint64, int, error) {
	if len(data) == 0 || data[0] == 0 {
		return 0, 0, io.ErrUnexpectedEOF
	}
	width := 1
	for width < 8 && data[0]&(0x80>>(width-1)) == 0 {
		width++
	}
	if len(data) < width {
		return 0, width, io.ErrUnexpectedEOF
	}
	var v uint64
	for i := 0; i < width; i++ {
		if i == 0 {
			v = uint64(data[0] & (0x7F >> (width - 1)))
			continue
		}
		v = (v << 8) | uint64(data[i])
	}
	return v, width, nil
}
