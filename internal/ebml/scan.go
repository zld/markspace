package ebml

import (
	"io"

	"mediaqc/internal/timeline"
)

// TrackInfo describes one track discovered during scanning.
type TrackInfo struct {
	Number uint64 `json:"number"`
	Type   uint64 `json:"type"` // 1 = video, 2 = audio (Matroska TrackType)
	Codec  string `json:"codec"`
}

// Kind maps the Matroska track type to the timeline track kind.
func (t TrackInfo) Kind() timeline.TrackKind {
	switch t.Type {
	case 1:
		return timeline.KindVideo
	case 2:
		return timeline.KindAudio
	default:
		return timeline.KindOther
	}
}

// BlockInfo locates one block in the input stream.
type BlockInfo struct {
	Offset      int64  `json:"offset"`       // absolute offset of the element
	Size        int64  `json:"size"`         // element total length
	Track       uint64 `json:"track"`        // 0 when the block header is unreadable
	RawTimecode int64  `json:"raw_timecode"` // cluster timestamp + block timecode (ticks)
	Keyframe    bool   `json:"keyframe"`
	// PayloadOff/PayloadLen locate the frame data inside the input.
	PayloadOff int64 `json:"payload_off"`
	PayloadLen int64 `json:"payload_len"`
}

// ScanResult is the product of the scanning stage.
type ScanResult struct {
	TimestampScale uint64      `json:"timestamp_scale"` // ns per tick
	Tracks         []TrackInfo `json:"tracks"`
	Blocks         []BlockInfo `json:"blocks"`
}

// Scan walks the EBML stream of size bytes from r, collecting tracks
// and block boundaries. Unknown-size Segment and Cluster elements are
// bounded by their parent (or end of file). Truncation and oversized
// elements are reported as positioned faults and abort the scan.
func Scan(r io.ReaderAt, size int64) (*ScanResult, error) {
	res := &ScanResult{TimestampScale: timeline.DefaultTickNs}
	off := int64(0)
	for off < size {
		el, err := ReadElement(r, off, size)
		if err != nil {
			return nil, err
		}
		switch el.ID {
		case IDSegment:
			if err := scanSegment(r, el.End(size), el.DataOff, res); err != nil {
				return nil, err
			}
			return res, nil
		default:
			// EBML header and anything else at top level: skip.
			if el.Size == UnknownSize {
				return nil, timeline.NewFault(timeline.ClassSizeOverflow, off, 0,
					"unknown-size top-level element 0x%x", el.ID)
			}
		}
		off = el.End(size)
	}
	return nil, timeline.NewFault(timeline.ClassTruncated, 0, 0,
		"no Segment element found in %d bytes", size)
}

func scanSegment(r io.ReaderAt, end, off int64, res *ScanResult) error {
	for off < end {
		el, err := ReadElement(r, off, end)
		if err != nil {
			return err
		}
		elEnd := el.End(end)
		switch el.ID {
		case IDInfo:
			if err := scanInfo(r, el, elEnd, res); err != nil {
				return err
			}
		case IDTracks:
			if err := scanTracks(r, el, elEnd, res); err != nil {
				return err
			}
		case IDCluster:
			if err := scanCluster(r, el, elEnd, res); err != nil {
				return err
			}
		}
		off = elEnd
	}
	return nil
}

func scanInfo(r io.ReaderAt, info Element, end int64, res *ScanResult) error {
	for off := info.DataOff; off < end; {
		el, err := ReadElement(r, off, end)
		if err != nil {
			return err
		}
		if el.ID == IDTimestampScale {
			v, err := ReadUint(r, el)
			if err != nil {
				return err
			}
			res.TimestampScale = v
		}
		off = el.End(end)
	}
	return nil
}

func scanTracks(r io.ReaderAt, tracks Element, end int64, res *ScanResult) error {
	for off := tracks.DataOff; off < end; {
		el, err := ReadElement(r, off, end)
		if err != nil {
			return err
		}
		if el.ID == IDTrackEntry {
			ti, err := scanTrackEntry(r, el, el.End(end))
			if err != nil {
				return err
			}
			res.Tracks = append(res.Tracks, ti)
		}
		off = el.End(end)
	}
	return nil
}

func scanTrackEntry(r io.ReaderAt, entry Element, end int64) (TrackInfo, error) {
	var ti TrackInfo
	for off := entry.DataOff; off < end; {
		el, err := ReadElement(r, off, end)
		if err != nil {
			return ti, err
		}
		switch el.ID {
		case IDTrackNumber:
			v, err := ReadUint(r, el)
			if err != nil {
				return ti, err
			}
			ti.Number = v
		case IDTrackType:
			v, err := ReadUint(r, el)
			if err != nil {
				return ti, err
			}
			ti.Type = v
		case IDCodecID:
			s, err := ReadString(r, el)
			if err != nil {
				return ti, err
			}
			ti.Codec = s
		}
		off = el.End(end)
	}
	return ti, nil
}

func scanCluster(r io.ReaderAt, cluster Element, end int64, res *ScanResult) error {
	var clusterTS int64
	for off := cluster.DataOff; off < end; {
		el, err := ReadElement(r, off, end)
		if err != nil {
			return err
		}
		elEnd := el.End(end)
		switch el.ID {
		case IDTimestamp:
			v, err := ReadUint(r, el)
			if err != nil {
				return err
			}
			clusterTS = int64(v)
		case IDSimpleBlock:
			if err := scanBlock(r, el, clusterTS, true, res); err != nil {
				return err
			}
		case IDBlockGroup:
			if err := scanBlockGroup(r, el, elEnd, clusterTS, res); err != nil {
				return err
			}
		}
		off = elEnd
	}
	return nil
}

func scanBlockGroup(r io.ReaderAt, group Element, end int64, clusterTS int64, res *ScanResult) error {
	for off := group.DataOff; off < end; {
		el, err := ReadElement(r, off, end)
		if err != nil {
			return err
		}
		if el.ID == IDBlock {
			if err := scanBlock(r, el, clusterTS, false, res); err != nil {
				return err
			}
		}
		off = el.End(end)
	}
	return nil
}

// scanBlock reads one block element's payload (bounded by the element
// size, never the whole file) and records its boundary.
func scanBlock(r io.ReaderAt, el Element, clusterTS int64, simple bool, res *ScanResult) error {
	if el.Size == UnknownSize || el.Size > maxBlockPayload {
		return timeline.NewFault(timeline.ClassSizeOverflow, el.DataOff, 0,
			"block element 0x%x has unusable size %d", el.ID, el.Size)
	}
	data := make([]byte, el.Size)
	if _, err := r.ReadAt(data, el.DataOff); err != nil {
		return timeline.NewFault(timeline.ClassTruncated, el.DataOff, 0, "%v", err)
	}
	blk, err := ParseBlock(data, el.DataOff, simple)
	if err != nil {
		return err
	}
	res.Blocks = append(res.Blocks, BlockInfo{
		Offset:      el.DataOff - (el.TotalLen - el.Size),
		Size:        el.TotalLen,
		Track:       blk.Track,
		RawTimecode: clusterTS + int64(blk.Timecode),
		Keyframe:    blk.Keyframe,
		PayloadOff:  blk.PayloadOff,
		PayloadLen:  blk.PayloadLen,
	})
	return nil
}
