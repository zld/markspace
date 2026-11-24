package ebml

import (
	"encoding/binary"
	"hash/crc32"
	"io"

	"mediacheck/timeline"
)

func Scan(r ReadSeekerAt) (*ScanResult, error) {
	res := &ScanResult{TimeScaleNs: 1_000_000, TruncatedAt: -1}
	er := NewReader(r)
	return res, scanRoot(er, res)
}

func scanRoot(er *Reader, res *ScanResult) error {
	for {
		h, err := er.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return finishTrunc(res, err)
		}
		if h.ID != IDSegment {
			if err := er.Skip(h); err != nil {
				return finishTrunc(res, err)
			}
			continue
		}
		res.SegmentOff = h.Offset
		res.SegmentLive = h.Size == UnknownSize
		if err := scanSegment(er, h, res); err != nil {
			return err
		}
	}
}

func scanSegment(er *Reader, seg *Header, res *ScanResult) error {
	if err := er.Enter(seg); err != nil {
		return err
	}
	defer er.Exit()
	for {
		h, err := er.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return finishTrunc(res, err)
		}
		switch h.ID {
		case IDInfo:
			if err := scanInfo(er, h, res); err != nil {
				return finishTrunc(res, err)
			}
		case IDTracks:
			if err := scanTracks(er, h, res); err != nil {
				return finishTrunc(res, err)
			}
		case IDCluster:
			if err := scanCluster(er, h, res); err != nil {
				return finishTrunc(res, err)
			}
		case IDContainerCol:
			cm, err := parseColor(er, h)
			if err != nil {
				return finishTrunc(res, err)
			}
			res.ContainerColor = cm
		default:
			if err := er.Skip(h); err != nil {
				return finishTrunc(res, err)
			}
		}
	}
}

func scanInfo(er *Reader, h *Header, res *ScanResult) error {
	payload, err := er.ReadPayload(h)
	if err != nil {
		return err
	}
	els, err := childElements(h.DataOffset, payload)
	if err != nil {
		return err
	}
	for _, e := range els {
		if e.header.ID == IDTimeScale {
			res.TimeScaleNs = int64(binary.BigEndian.Uint64(e.payload))
		}
	}
	return nil
}

func scanTracks(er *Reader, h *Header, res *ScanResult) error {
	payload, err := er.ReadPayload(h)
	if err != nil {
		return err
	}
	els, err := childElements(h.DataOffset, payload)
	if err != nil {
		return err
	}
	for _, e := range els {
		if e.header.ID != IDTrackEntry {
			continue
		}
		if len(res.Tracks) >= MaxTracks {
			return timeline.New(timeline.CatOverflow, "track count exceeds limit").
				WithOffset(e.header.Offset)
		}
		t, err := parseTrack(e)
		if err != nil {
			return err
		}
		res.Tracks = append(res.Tracks, t)
	}
	return nil
}

func scanCluster(er *Reader, h *Header, res *ScanResult) error {
	// Unknown-size clusters are read byte-by-byte until stop ID / EOF;
	// known-size clusters can be CRC-checked as one contiguous payload.
	if h.Size == UnknownSize {
		return scanLiveCluster(er, h, res)
	}
	payload, err := er.ReadPayload(h)
	if err != nil {
		return err
	}
	els, err := childElements(h.DataOffset, payload)
	if err != nil {
		return err
	}
	checkOK := true
	for _, e := range els {
		if e.header.ID == IDCRC32 {
			if !verifyClusterCRC(els, e.payload) {
				checkOK = false
				res.CheckFailures = append(res.CheckFailures, h.Offset)
			}
		}
	}
	clusterTime := int64(-1)
	for _, e := range els {
		if e.header.ID == IDClusterTime {
			clusterTime = int64(binary.BigEndian.Uint64(e.payload))
		}
	}
	for _, e := range els {
		if e.header.ID != IDSimpleBlock {
			continue
		}
		if err := appendBlock(res, e.header, e.payload, h.Offset, clusterTime, checkOK); err != nil {
			return err
		}
	}
	return nil
}

func scanLiveCluster(er *Reader, h *Header, res *ScanResult) error {
	if err := er.Enter(h); err != nil {
		return err
	}
	defer er.Exit()
	clusterTime := int64(-1)
	for {
		ch, err := er.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return finishTrunc(res, err)
		}
		switch ch.ID {
		case IDClusterTime:
			p, err := er.ReadPayload(ch)
			if err != nil {
				return finishTrunc(res, err)
			}
			clusterTime = int64(binary.BigEndian.Uint64(p))
		case IDSimpleBlock:
			p, err := er.ReadPayload(ch)
			if err != nil {
				return finishTrunc(res, err)
			}
			if err := appendBlock(res, ch, p, h.Offset, clusterTime, true); err != nil {
				return err
			}
		default:
			if err := er.Skip(ch); err != nil {
				return finishTrunc(res, err)
			}
		}
	}
	return nil
}

func appendBlock(res *ScanResult, h *Header, payload []byte, clusterOff, clusterTime int64, checkOK bool) error {
	if len(payload) < 4 {
		return timeline.New(timeline.CatCorrupt, "block header too short").WithOffset(h.Offset)
	}
	// Fixture convention: the block track number is a numeric VINT (same
	// encoding as a data-size VINT), so the marker bits are stripped.
	trackNum, _, err := decodeVINT(payload[:vintLen(payload[0])], false)
	if err != nil {
		return timeline.New(timeline.CatCorrupt, "block track number invalid").WithOffset(h.Offset)
	}
	rel := int16(binary.BigEndian.Uint16(payload[1:3]))
	absNs, ok := blockTimeNs(res.TimeScaleNs, clusterTime, int64(rel))
	if !ok {
		return timeline.New(timeline.CatOverflow, "block timestamp overflow").WithOffset(h.Offset)
	}
	res.Blocks = append(res.Blocks, &BlockInfo{
		TrackNumber: uint32(trackNum),
		TimeNs:      absNs,
		Offset:      h.Offset,
		Size:        h.Size,
		Payload:     payload[3:],
		ClusterOff:  clusterOff,
		ClusterTime: clusterTime,
		CheckOK:     checkOK,
	})
	res.BlockCount++
	return nil
}

func blockTimeNs(scale, cluster, rel int64) (int64, bool) {
	total := cluster + rel
	if (cluster^total)&(rel^total) < 0 {
		return 0, false // signed addition overflow
	}
	if total < 0 {
		return 0, false
	}
	return mulInt64(scale, total)
}

func mulInt64(a, b int64) (int64, bool) {
	if a < 0 || b < 0 {
		return 0, false
	}
	hi, lo := mul64u(uint64(a), uint64(b))
	if hi != 0 {
		return 0, false
	}
	return int64(lo), true
}

// verifyClusterCRC recomputes CRC-32 (IEEE) over every child element except
// the CRC element itself and compares with the stored little-endian value.
func verifyClusterCRC(els []element, stored []byte) bool {
	if len(stored) < 4 {
		return false
	}
	h := crc32.NewIEEE()
	for _, e := range els {
		if e.header.ID == IDCRC32 {
			continue
		}
		raw, err := e.raw()
		if err != nil {
			return false
		}
		h.Write(raw)
	}
	return binary.LittleEndian.Uint32(stored[:4]) == h.Sum32()
}

// finishTrunc records the fault offset on the partial result so the index
// built so far can be retained for review.
func finishTrunc(res *ScanResult, err error) error {
	if ce, ok := err.(*timeline.Error); ok && ce.Offset >= 0 {
		res.TruncatedAt = ce.Offset
	} else {
		res.TruncatedAt = -2
	}
	return err
}
