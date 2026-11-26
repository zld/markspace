package ebml

import (
	"fmt"
	"math"

	"pwflow/internal/fault"
)

// Default limits keep malformed input from causing unbounded work.
const (
	DefaultMaxBytes       int64 = 256 << 20
	DefaultMaxTracks            = 16
	DefaultMaxResyncBytes int64 = 4 << 20
)

// Limits bounds input size, track count and corruption resync scanning.
type Limits struct {
	MaxBytes       int64
	MaxTracks      int
	MaxResyncBytes int64
}

// DefaultLimits returns the safe local parsing limits.
func DefaultLimits() Limits {
	return Limits{
		MaxBytes:       DefaultMaxBytes,
		MaxTracks:      DefaultMaxTracks,
		MaxResyncBytes: DefaultMaxResyncBytes,
	}
}

// Track describes the subset of a Matroska TrackEntry used by pwflow.
type Track struct {
	Number uint64 `json:"number"`
	Type   uint64 `json:"type"`
	Codec  string `json:"codec,omitempty"`
	Width  uint64 `json:"width,omitempty"`
	Height uint64 `json:"height,omitempty"`
}

// Block is a parsed and time-mapped SimpleBlock or Block.
type Block struct {
	ElementOffset      int64  `json:"element_offset"`
	DataOffset         int64  `json:"data_offset"`
	TrackNumber        uint64 `json:"track_number"`
	ClusterTimecodeNS  int64  `json:"cluster_timecode_ns"`
	RelativeTimecodeMS int16  `json:"relative_timecode"`
	PTSNS              int64  `json:"pts_ns"`
	Keyframe           bool   `json:"keyframe"`
	Simple             bool   `json:"simple"`
	PayloadBytes       int    `json:"payload_bytes"`
}

// Damage is a recoverable bad block and the byte range skipped after a
// following Cluster was positively confirmed.
type Damage struct {
	fault.Record
	SkippedTo int64 `json:"skipped_to"`
}

// Report is the result of best-effort EBML parsing.
type Report struct {
	TimecodeScaleNS int64    `json:"timecode_scale_ns"`
	Tracks          []Track  `json:"tracks"`
	Blocks          []Block  `json:"blocks"`
	Damages         []Damage `json:"damages"`
	UnknownSegment  bool     `json:"unknown_segment_length"`
	UnknownClusters int      `json:"unknown_clusters"`
}

type parser struct {
	data   []byte
	limits Limits
	rep    Report
}

// Parse parses the Matroska/WebM subset needed by pwflow. Recoverable bad
// blocks are attached to Report.Damages. A non-nil error means safe recovery
// was impossible; the report may still contain earlier observations.
func Parse(data []byte, limits Limits) (*Report, error) {
	if limits.MaxBytes <= 0 {
		limits = DefaultLimits()
	}
	if int64(len(data)) > limits.MaxBytes {
		return nil, fault.New(fault.InputTooLarge, "input %d bytes exceeds limit %d", len(data), limits.MaxBytes)
	}
	p := &parser{data: data, limits: limits, rep: Report{TimecodeScaleNS: 1_000_000}}
	if len(data) == 0 {
		return nil, fault.New(fault.UnsupportedFormat, "empty EBML input")
	}

	off := int64(0)
	sawSegment := false
	for off < int64(len(data)) {
		h, err := ReadHeader(data, off)
		if err != nil {
			d, ferr := p.resyncCluster(off, fault.EBMLHeader, err.Error())
			if ferr != nil {
				return &p.rep, ferr
			}
			p.rep.Damages = append(p.rep.Damages, d)
			off = d.SkippedTo
			continue
		}
		end, ok := safeEnd(h, int64(len(data)))
		if !ok {
			d, ferr := p.resyncCluster(off, fault.EBMLVINT, "element size exceeds file")
			if ferr != nil {
				return &p.rep, ferr
			}
			p.rep.Damages = append(p.rep.Damages, d)
			off = d.SkippedTo
			continue
		}
		switch h.ID {
		case IDEBMLHeader:
			off = end
		case IDSegment:
			sawSegment = true
			p.rep.UnknownSegment = h.Unknown
			next, err := p.parseSegment(h, end)
			if err != nil {
				return &p.rep, err
			}
			if next > off {
				off = next
			} else {
				off = end
			}
		default:
			off = end
		}
	}
	if !sawSegment {
		return &p.rep, fault.New(fault.UnsupportedFormat, "EBML Segment not found")
	}
	return &p.rep, nil
}

func safeEnd(h Header, fileLen int64) (int64, bool) {
	if h.Unknown {
		return fileLen, true
	}
	if h.DataOffset < 0 || h.Size > math.MaxInt64-uint64(h.DataOffset) {
		return 0, false
	}
	end := h.DataOffset + int64(h.Size)
	if end > fileLen {
		return fileLen, false
	}
	return end, true
}

func (p *parser) parseSegment(h Header, segEnd int64) (int64, error) {
	off := h.DataOffset
	for off < segEnd {
		eh, err := ReadHeader(p.data, off)
		if err != nil {
			d, rerr := p.resyncCluster(off, fault.EBMLHeader, err.Error())
			if rerr != nil {
				return off, rerr
			}
			p.rep.Damages = append(p.rep.Damages, d)
			off = d.SkippedTo
			continue
		}
		end, ok := safeEnd(eh, segEnd)
		if !ok {
			d, rerr := p.resyncCluster(off, fault.EBMLVINT, "child size exceeds Segment")
			if rerr != nil {
				return off, rerr
			}
			p.rep.Damages = append(p.rep.Damages, d)
			off = d.SkippedTo
			continue
		}
		switch eh.ID {
		case IDInfo:
			if err := p.parseInfo(eh.DataOffset, end); err != nil {
				return off, err
			}
		case IDTracks:
			if err := p.parseTracks(eh.DataOffset, end); err != nil {
				return off, err
			}
		case IDCluster:
			next, err := p.parseCluster(eh, end)
			if err != nil {
				return off, err
			}
			if next > off {
				off = next
				continue
			}
		}
		off = end
	}
	return segEnd, nil
}

func (p *parser) parseInfo(start, end int64) error {
	return eachChild(p.data, start, end, func(h Header, bodyEnd int64) error {
		if h.ID == IDTimecodeScale {
			if h.Unknown || bodyEnd-h.DataOffset > 8 {
				return fault.At(fault.EBMLVINT, h.DataOffset, "invalid TimecodeScale")
			}
			p.rep.TimecodeScaleNS = int64(readUint(p.data[h.DataOffset:bodyEnd]))
			if p.rep.TimecodeScaleNS <= 0 {
				p.rep.TimecodeScaleNS = 1_000_000
			}
		}
		return nil
	})
}

func eachChild(data []byte, start, end int64, fn func(h Header, bodyEnd int64) error) error {
	off := start
	for off < end {
		h, err := ReadHeader(data, off)
		if err != nil {
			return err
		}
		bodyEnd, ok := safeEnd(h, end)
		if !ok {
			return fmt.Errorf("element %x at %d exceeds parent", h.ID, off)
		}
		if err := fn(h, bodyEnd); err != nil {
			return err
		}
		off = bodyEnd
	}
	return nil
}
