package bitstream

import "mediacore/internal/fault"

// scanAV1 walks the low-overhead OBU stream. A coded picture is one frame OBU
// plus the preamble that precedes it. Pictures tile the stream: a picture
// starts at an OBU that owns framing (a sequence header that is not already
// part of the previous picture's key frame, or a standalone frame header) or,
// failing that, at the most recent temporal delimiter. A standalone frame
// header that sets show_existing_frame only redisplays an earlier picture and
// opens no coded picture. A sequence header must precede the first frame.
func scanAV1(data []byte) ([]Frame, bool, error) {
	obus, err := splitOBU(data)
	if err != nil {
		return nil, false, err
	}
	var frames []Frame
	reducedStill := false
	haveSeq := false
	var cur *av1Picture
	lastTD := int64(-1)
	lastFH := int64(-1)
	lastPrefix := int64(-1)

	flush := func(boundary int64) {
		if cur == nil {
			return
		}
		if boundary <= cur.start {
			boundary = cur.start + 1
		}
		if boundary > int64(len(data)) {
			boundary = int64(len(data))
		}
		frames = append(frames, Frame{
			Offset:     cur.start,
			Size:       boundary - cur.start,
			Keyframe:   cur.key,
			TemporalID: cur.temporal,
		})
		cur = nil
		lastTD, lastFH, lastPrefix = -1, -1, -1
	}

	for _, o := range obus {
		switch o.typ {
		case obuSequenceHeader:
			if len(o.payload) < 2 {
				return nil, false, truncated(CodecAV1, o.offset, "sequence header")
			}
			haveSeq = true
			reducedStill = o.payload[0]&0x08 != 0
			// A sequence header immediately before a frame belongs to that
			// frame. After a complete picture (the key frame that carried the
			// header) a new header opens the next picture.
			if lastPrefix < 0 {
				lastPrefix = o.offset
			}
		case obuTemporalDelim:
			lastTD = o.offset
			if lastPrefix < 0 {
				lastPrefix = o.offset
			}
		case obuFrameHeader, obuFrame:
			if !haveSeq {
				return nil, false, fault.New(fault.CodeScanMissingParameter, o.offset, string(CodecAV1), "frame precedes any sequence header")
			}
			showExisting, key, err := av1FrameInfo(o.payload, reducedStill, o.offset)
			if err != nil {
				return nil, false, err
			}
			if o.typ == obuFrameHeader {
				if !showExisting && lastFH < 0 {
					lastFH = o.offset
					if lastPrefix < 0 {
						lastPrefix = o.offset
					}
				}
				continue
			}
			// Choose the latest framing OBU seen since the previous picture.
			start := lastPrefix
			for _, candidate := range []int64{lastTD, lastFH} {
				if candidate > start {
					start = candidate
				}
			}
			if start < 0 || start >= o.offset {
				start = o.offset
			}
			if cur != nil && start <= cur.start {
				start = o.offset
			}
			flush(start)
			cur = &av1Picture{start: start, key: key, temporal: o.temporal}
			lastTD, lastFH, lastPrefix = -1, -1, -1
		default:
			if lastPrefix < 0 {
				lastPrefix = o.offset
			}
		}
	}
	flush(int64(len(data)))
	if len(frames) == 0 {
		return nil, false, fault.New(fault.CodeScanNoFrames, 0, string(CodecAV1), "no frames found")
	}
	return frames, haveSeq, nil
}

// startOf returns the start of the open picture, or a sentinel that compares
// below any real offset when no picture is open.
func startOf(p *av1Picture) int64 {
	if p == nil {
		return -1
	}
	return p.start
}
