// Package analyze inspects local MP4 fragments, RTP packet streams and the
// resulting bitrate/quality statistics. Every finding carries the byte offset
// or packet index it came from so a damaged region can be located later.
package analyze

import (
	"mediamux/media"
)

// SegmentResult is one analysed media segment.
type SegmentResult struct {
	Index        int             `json:"index"`
	ID           string          `json:"id"`
	Node         string          `json:"node"`
	Bytes        int64           `json:"bytes"`
	BytesOffset  int64           `json:"bytes_offset"`
	SHA256       string          `json:"sha256"`
	Boxes        []BoxRecord     `json:"boxes,omitempty"`
	Tracks       []TrackInfo     `json:"tracks,omitempty"`
	Samples      []SampleRecord  `json:"samples,omitempty"`
	Keyframes    []int64         `json:"keyframes,omitempty"`
	KeyframeGaps []int64         `json:"keyframe_gaps_ms,omitempty"`
	Duration     media.Time      `json:"duration"`
	Corrupt      bool            `json:"corrupt"`
	Damaged      []DamagedRegion `json:"damaged,omitempty"`
	HasIndex     bool            `json:"has_index"`
	Recovery     string          `json:"recovery,omitempty"`
}

// BoxRecord is one box the scanner saw.
type BoxRecord struct {
	Type   string `json:"type"`
	Offset int64  `json:"offset"`
	Size   int64  `json:"size"`
	Depth  int    `json:"depth"`
}

// TrackInfo summarises one moov/trak or moof/traf.
type TrackInfo struct {
	ID        uint32 `json:"id"`
	Handler   string `json:"handler,omitempty"`
	Timescale int64  `json:"timescale"`
	Duration  int64  `json:"duration"`
	Samples   int    `json:"samples"`
}

// SampleRecord is one sample's timing and size.
type SampleRecord struct {
	TrackID    uint32 `json:"track_id"`
	Index      int    `json:"index"`
	Size       uint32 `json:"size"`
	Duration   uint32 `json:"duration"`
	Keyframe   bool   `json:"keyframe"`
	DecodeTime int64  `json:"decode_time"`
}

// DamagedRegion marks a byte range that could not be trusted.
type DamagedRegion struct {
	Offset int64      `json:"offset"`
	Length int64      `json:"length"`
	Reason string     `json:"reason"`
	Code   media.Code `json:"code"`
}

// MP4Options tunes the scanner.
type MP4Options struct {
	// MaxBoxes bounds how many boxes are recorded, keeping memory fixed.
	MaxBoxes int
	// TrackBoxes records every box in the result for evidence output.
	TrackBoxes bool
}

type scanState struct {
	res      *SegmentResult
	tracks   []*TrackInfo
	maxBoxes int
	count    int
}

// AnalyzeMP4 scans a segment using go-mp4. A syntactically invalid box length
// is recorded as a damaged region with its absolute offset, and scanning
// resumes at the next byte-aligned box boundary it can trust. A missing sample
// index marks the segment damaged but still returns the recovered structure.
func AnalyzeMP4(idx int, id, node string, data []byte, baseOffset int64, opts MP4Options) *SegmentResult {
	if opts.MaxBoxes <= 0 {
		opts.MaxBoxes = 8192
	}
	res := &SegmentResult{Index: idx, ID: id, Node: node, Bytes: int64(len(data)), BytesOffset: baseOffset}
	res.SHA256 = Digest(data)
	st := &scanState{res: res, maxBoxes: opts.MaxBoxes}
	st.scan(data, baseOffset, 0, int64(len(data)), 0, nil, opts.TrackBoxes)
	for _, t := range st.tracks {
		t.Samples = len(res.Samples)
		res.Tracks = append(res.Tracks, *t)
	}
	for i := range res.Samples {
		res.Samples[i].TrackID = trackIDFor(res.Tracks, res.Samples[i].TrackID)
	}
	res.HasIndex = len(res.Samples) > 0
	res.Corrupt = len(res.Damaged) > 0
	if !res.HasIndex && len(res.Tracks) > 0 {
		res.Corrupt = true
		res.Damaged = append(res.Damaged, DamagedRegion{
			Offset: baseOffset, Length: int64(len(data)),
			Reason: "segment has tracks but no sample index", Code: media.CodeMissingIndex,
		})
	}
	if res.Corrupt {
		res.Recovery = "resumed at next valid box boundary"
	}
	finishSegment(res)
	return res
}

// finishSegment derives duration, keyframe gaps and damage counters.
func finishSegment(res *SegmentResult) {
	var totalDur int64
	ts := media.Timescale90K
	if len(res.Tracks) > 0 && res.Tracks[0].Timescale > 0 {
		ts = media.Timescale(res.Tracks[0].Timescale)
	}
	for _, s := range res.Samples {
		totalDur += int64(s.Duration)
	}
	if totalDur > 0 {
		res.Duration = media.NewTime(scaleTicks(totalDur, ts, media.Timescale90K), media.Timescale90K)
	}
	res.KeyframeGaps = keyframeGapsMS(res.Keyframes, res.Samples, ts)
	if len(res.Keyframes) == 0 && len(res.Samples) > 0 {
		// A fragment without any sync sample cannot anchor playback.
		res.Damaged = append(res.Damaged, DamagedRegion{
			Offset: res.BytesOffset, Length: res.Bytes, Reason: "no sync sample", Code: media.CodeMissingIndex,
		})
		res.Corrupt = true
	}
}

// scan walks boxes in file order. Containers recurse immediately, so each
// track's children are attributed to the correct enclosing trak or traf.
func (st *scanState) scan(data []byte, base, start, end int64, depth int, track *TrackInfo, trackBoxes bool) {
	for off := start; off+8 <= end; {
		if st.count >= st.maxBoxes {
			st.res.Damaged = append(st.res.Damaged, DamagedRegion{
				Offset: base + off, Length: end - off,
				Reason: "box count exceeds analysis budget", Code: media.CodeInput,
			})
			return
		}
		bi, err := readBoxAt(data, off, end)
		if err != nil {
			st.res.Damaged = append(st.res.Damaged, DamagedRegion{
				Offset: base + off, Length: end - off,
				Reason: err.Error(), Code: media.CodeBoxOverflow,
			})
			next, ok := recoverBoundary(data, off, end)
			if !ok {
				return
			}
			off = next
			continue
		}
		st.count++
		typ := bi.Type.String()
		if trackBoxes {
			st.res.Boxes = append(st.res.Boxes, BoxRecord{
				Type: typ, Offset: base + int64(bi.Offset), Size: int64(bi.Size), Depth: depth,
			})
		}
		payloadStart := int64(bi.Offset + bi.HeaderSize)
		payloadEnd := int64(bi.Offset + bi.Size)
		switch typ {
		case "trak", "traf":
			t := &TrackInfo{}
			st.tracks = append(st.tracks, t)
			st.scan(data, base, payloadStart, payloadEnd, depth+1, t, trackBoxes)
		case "moov", "mdia", "minf", "stbl", "mvex":
			st.scan(data, base, payloadStart, payloadEnd, depth+1, track, trackBoxes)
		default:
			parseLeaf(data, bi, track, st.res)
		}
		off = payloadEnd
	}
}
