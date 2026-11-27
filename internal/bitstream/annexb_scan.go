package bitstream

import "mediacore/internal/fault"

// scanAnnexB handles both AVC and HEVC. A new picture begins at a slice with
// the "first slice" flag set. Parameter sets read before the first picture are
// part of that picture's access unit.
func scanAnnexB(codec Codec, data []byte) ([]Frame, bool, error) {
	units, err := splitAnnexB(codec, data)
	if err != nil {
		return nil, false, err
	}
	var frames []Frame
	var cur *Frame
	var curKey bool
	groupStart := int64(-1)
	paramOK := false

	closeFrame := func(end int64) {
		if cur == nil {
			return
		}
		if end <= cur.Offset {
			end = cur.Offset
		}
		cur.Size = end - cur.Offset
		cur.Keyframe = curKey
		frames = append(frames, *cur)
		cur = nil
		curKey = false
	}

	for _, u := range units {
		if groupStart < 0 {
			groupStart = u.startCode
		}
		if len(u.payload) < 1 {
			return nil, false, truncated(codec, u.offset, "NAL header")
		}
		var first, key, parameter bool
		if codec == CodecH264 {
			typ := int(u.payload[0] & 0x1F)
			parameter = typ == 7 || typ == 8
			key = typ == 5
			switch typ {
			case 1, 2, 5:
				first, err = h264FirstSlice(u.payload[1:], u.offset+1)
				if err != nil {
					return nil, false, err
				}
			case 12: // filler data
				continue
			}
		} else {
			if len(u.payload) < 2 {
				return nil, false, truncated(codec, u.offset, "NAL header")
			}
			typ := int(u.payload[0]>>1) & 0x3F
			parameter = typ == 32 || typ == 33 || typ == 34
			key = typ >= 16 && typ <= 21
			switch {
			case typ < 32: // VCL
				first, err = h265FirstSlice(u.payload[2:], u.offset+2)
				if err != nil {
					return nil, false, err
				}
			case typ == 38: // filler data
				continue
			}
		}
		if parameter {
			paramOK = true
		}
		if !first {
			continue
		}
		closeFrame(groupStart)
		cur = &Frame{Offset: groupStart}
		curKey = key
		groupStart = -1
	}
	closeFrame(int64(len(data)))
	if len(frames) == 0 {
		return nil, false, fault.New(fault.CodeScanNoFrames, 0, string(codec), "no pictures found")
	}
	return frames, paramOK, nil
}
