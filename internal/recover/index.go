// Package recover rebuilds confirmable boundaries from damaged MP4 inputs.
package recover

import (
	"encoding/binary"
	"math"

	"github.com/zld/mediacore/internal/model"
	"github.com/zld/mediacore/internal/trace"
)

// SampleMagic identifies the self-describing sample boxes used by fixtures.
var SampleMagic = [4]byte{'M', 'C', 'S', '1'}

// SampleHeaderSize is the fixed MCS1 payload header before media bytes.
const SampleHeaderSize = 28

// Box is a scanned top-level MP4 box boundary.
type Box struct {
	Type         string `json:"type"`
	Offset       int64  `json:"offset"`
	HeaderSize   int64  `json:"header_size"`
	DeclaredSize int64  `json:"declared_size"`
	DeclaredEnd  int64  `json:"declared_end"`
	Complete     bool   `json:"complete"`
}

// ScanReport contains parseable boxes and the first boundary failure.
type ScanReport struct {
	FileSize int64        `json:"file_size"`
	Boxes    []Box        `json:"boxes"`
	Failure  *trace.Error `json:"failure,omitempty"`
}

// ScanRootBoxes scans top-level MP4 boxes without depending on an index.
// A truncated final box is included as incomplete and an INPUT_TRUNCATED error
// is returned at the actual EOF offset. An impossible declared length returns
// BOX_LENGTH_OUT_OF_BOUNDS at the box start.
func ScanRootBoxes(data []byte) (*ScanReport, error) {
	size := int64(len(data))
	rep := &ScanReport{FileSize: size}
	var scanErr *trace.Error

	for off := int64(0); off < size; {
		if size-off < 8 {
			scanErr = trace.E(trace.CodeTruncated, trace.StageRecover, "short box header: %d byte(s) remain", size-off).WithOffset(size)
			rep.Failure = scanErr
			return rep, scanErr
		}

		size32 := binary.BigEndian.Uint32(data[off : off+4])
		boxType := string(data[off+4 : off+8])
		headerSize := int64(8)
		declared := int64(size32)

		if size32 == 1 {
			if size-off < 16 {
				scanErr = trace.E(trace.CodeTruncated, trace.StageRecover, "short 64-bit box header").WithOffset(size)
				rep.Failure = scanErr
				return rep, scanErr
			}
			large := binary.BigEndian.Uint64(data[off+8 : off+16])
			if large > math.MaxInt64 {
				scanErr = trace.E(trace.CodeBoxLengthOutOfBounds, trace.StageRecover, "64-bit box size overflows int64: %d", large).WithOffset(off)
				rep.Failure = scanErr
				return rep, scanErr
			}
			declared = int64(large)
			headerSize = 16
		} else if size32 == 0 {
			declared = size - off
		}

		if declared < headerSize {
			scanErr = trace.E(trace.CodeBoxLengthOutOfBounds, trace.StageRecover, "box %q declared size %d < header %d", boxType, declared, headerSize).WithOffset(off)
			rep.Failure = scanErr
			return rep, scanErr
		}
		if off > math.MaxInt64-declared {
			scanErr = trace.E(trace.CodeBoxLengthOutOfBounds, trace.StageRecover, "box %q endpoint overflows", boxType).WithOffset(off)
			rep.Failure = scanErr
			return rep, scanErr
		}

		end := off + declared
		box := Box{Type: boxType, Offset: off, HeaderSize: headerSize, DeclaredSize: declared, DeclaredEnd: end}
		if end > size {
			box.Complete = false
			rep.Boxes = append(rep.Boxes, box)
			scanErr = trace.E(trace.CodeTruncated, trace.StageRecover, "box %q needs %d bytes but file ends at %d", boxType, end, size).WithOffset(size)
			rep.Failure = scanErr
			return rep, scanErr
		}

		box.Complete = true
		rep.Boxes = append(rep.Boxes, box)
		off = end
	}
	return rep, nil
}

// TrackMeta supplies timescale information extracted from MP4 metadata.
type TrackMeta struct {
	ID           int
	TimeScale    uint32
	Kind         string
	Handler      string
	OffsetNS     model.NS
	IndexPresent bool
}

// RebuildTracks converts complete MCS1 sample boxes into tracks. The final
// incomplete box, when its header is readable, becomes a located Damage.
func RebuildTracks(data []byte, rep *ScanReport, meta []TrackMeta) ([]*model.Track, []model.Damage) {
	byID := make(map[int]*model.Track, len(meta))
	order := make([]int, 0, len(meta))
	for _, m := range meta {
		tr := &model.Track{
			ID: m.ID, TimeScale: m.TimeScale, Kind: m.Kind, Handler: m.Handler,
			OffsetNS: m.OffsetNS, IndexPresent: m.IndexPresent,
		}
		byID[m.ID] = tr
		order = append(order, m.ID)
	}

	var damages []model.Damage
	for _, b := range rep.Boxes {
		if b.Type != "mcs " {
			continue
		}
		payloadOff := b.Offset + b.HeaderSize
		available := int64(len(data)) - payloadOff
		if available < SampleHeaderSize || payloadOff < 0 || payloadOff+SampleHeaderSize > int64(len(data)) {
			continue
		}
		hdr := data[payloadOff : payloadOff+SampleHeaderSize]
		if [4]byte(hdr[0:4]) != SampleMagic {
			continue
		}
		trackID := int(binary.BigEndian.Uint32(hdr[4:8]))
		index := int(binary.BigEndian.Uint32(hdr[8:12]))
		startTick := binary.BigEndian.Uint64(hdr[12:20])
		durationTick := binary.BigEndian.Uint32(hdr[20:24])
		crc := binary.BigEndian.Uint32(hdr[24:28])

		tr := byID[trackID]
		if tr == nil {
			tr = &model.Track{ID: trackID, IndexPresent: false}
			byID[trackID] = tr
			order = append(order, trackID)
		}

		dataStart := payloadOff + SampleHeaderSize
		s := &model.Sample{
			Index: index, TrackID: trackID, StartTick: startTick, DurationTick: durationTick,
			ExpectedCRC: crc, BoxOffset: b.Offset, PayloadOffset: payloadOff,
			Range:    model.ByteRange{Start: b.Offset, End: b.DeclaredEnd},
			Complete: b.Complete,
		}
		if b.Complete {
			s.Data = data[dataStart:b.DeclaredEnd]
			tr.Samples = append(tr.Samples, s)
			continue
		}

		dmg := model.Damage{
			TrackID: trackID,
			ByteOffset: func() int64 {
				if rep.Failure != nil && rep.Failure.HasByteOffset {
					return rep.Failure.ByteOffset
				}
				return int64(len(data))
			}(),
			Code:    trace.CodeTruncated,
			Stage:   trace.StageRecover,
			Message: "sample box is truncated",
			Error:   rep.Failure,
		}
		if tr.TimeScale > 0 {
			startNS, _ := model.TicksToNS(startTick, tr.TimeScale)
			endNS, _ := model.TicksToNS(startTick+uint64(durationTick), tr.TimeScale)
			dmg.Range = model.Interval{Start: tr.OffsetNS + startNS, End: tr.OffsetNS + endNS}
		}
		damages = append(damages, dmg)
	}

	tracks := make([]*model.Track, 0, len(order))
	for _, id := range order {
		tracks = append(tracks, byID[id])
	}
	return tracks, damages
}
