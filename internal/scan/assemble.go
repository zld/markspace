package scan

import "regress/internal/fault"

// assemble turns an ordered unit list (NAL units or OBUs) into complete
// access units and records every random access point.
//
// Annex B: an AUD closes the previous picture; a VCL NAL starts a new
// picture, and every non-VCL NAL preceding the first VCL NAL belongs to that
// picture. AV1: a temporal delimiter OBU starts a new picture; a sequence
// header is stashed and carried on the next picture as configuration data.
func assemble(codec string, units []NALUnit, streamBytes int64) (*ScanResult, error) {
	res := &ScanResult{Codec: codec, StreamBytes: streamBytes}
	var (
		cur   *AccessUnit
		cfg   []byte // most recent parameter sets / sequence header
		flush = func() {
			// Only units containing a coded picture become access units; a
			// leading AUD or parameter sets alone are stream configuration.
			if cur == nil || !hasVCL(cur) {
				cur = nil
				return
			}
			cur.Index = len(res.AccessUnits)
			if cur.IsRAP {
				cur.ConfigData = append([]byte(nil), cfg...)
				res.Keyframes = append(res.Keyframes, cur.Index)
			}
			res.AccessUnits = append(res.AccessUnits, *cur)
			cur = nil
		}
		ensure = func(offset int64) *AccessUnit {
			if cur == nil {
				cur = &AccessUnit{Offset: offset}
			}
			return cur
		}
	)

	switch codec {
	case CodecH264, CodecH265:
		for _, u := range units {
			switch u.Kind {
			case kindAUD:
				flush()
			case kindVCL:
				flush()
				au := ensure(u.Offset)
				au.NALs = append(au.NALs, u)
				au.IsRAP = u.IsRAP
				if u.IsRAP {
					cfg = cfg[:0]
				}
			case kindSPS, kindPPS, kindVPS:
				cfg = append(cfg, u.Data...)
				// Parameter sets before the first VCL NAL are stream-level
				// configuration, not an access unit of their own.
				if cur != nil {
					cur.NALs = append(cur.NALs, u)
				}
			default:
				// SEI and other non-VCL NALs attach to the pending picture.
				au := ensure(u.Offset)
				au.NALs = append(au.NALs, u)
			}
		}
	case CodecAV1:
		for _, u := range units {
			switch u.Kind {
			case kindTemporalDelimiter:
				flush()
			case kindSequenceHeader:
				flush()
				cfg = append(cfg[:0], u.Data...)
			case kindFrameHeader:
				au := ensure(u.Offset)
				au.NALs = append(au.NALs, u)
				au.IsRAP = u.IsRAP
			default:
				au := ensure(u.Offset)
				au.NALs = append(au.NALs, u)
			}
		}
	}
	flush()

	if len(res.AccessUnits) == 0 {
		return nil, fault.New(fault.StageScan, fault.CodeParse, codec,
			"no complete access unit found in elementary stream")
	}
	for i := range res.AccessUnits {
		au := &res.AccessUnits[i]
		end := streamBytes
		if i+1 < len(res.AccessUnits) {
			end = res.AccessUnits[i+1].Offset
		}
		au.Size = end - au.Offset
	}
	return res, nil
}

// hasVCL reports whether an in-progress access unit already contains a
// coded picture NAL.
func hasVCL(au *AccessUnit) bool {
	for _, n := range au.NALs {
		if n.Kind == kindVCL || n.Kind == kindFrameHeader {
			return true
		}
	}
	return false
}
