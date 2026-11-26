package scan

import "regress/internal/fault"

// assemble turns an ordered unit list (NAL units or OBUs) into complete
// access units and records every random access point.
//
// Annex B (H.264/H.265): a VCL NAL starts a new picture; an AUD only delimits
// pictures; parameter sets before the first VCL NAL are stream configuration.
//
// AV1 low-overhead: every temporal-delimiter OBU starts a new access unit. A
// sequence-header OBU is stashed as configuration and attached to the next
// keyframe. Frame-header OBUs carry the RAP (KEY_FRAME) decision.
func assemble(codec string, units []NALUnit, streamBytes int64) (*ScanResult, error) {
	res := &ScanResult{Codec: codec, StreamBytes: streamBytes}
	switch codec {
	case CodecH264, CodecH265:
		assembleAnnexB(res, units)
	case CodecAV1:
		assembleAV1(res, units)
	}
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

// assembleAnnexB groups NAL units into pictures keyed on VCL boundaries.
func assembleAnnexB(res *ScanResult, units []NALUnit) {
	var (
		cur *AccessUnit
		cfg []byte
	)
	flush := func() {
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
	ensure := func(offset int64) *AccessUnit {
		if cur == nil {
			cur = &AccessUnit{Offset: offset}
		}
		return cur
	}
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
			if cur != nil {
				cur.NALs = append(cur.NALs, u)
			}
		default:
			au := ensure(u.Offset)
			au.NALs = append(au.NALs, u)
		}
	}
	flush()
}

// assembleAV1 groups OBUs into access units keyed on temporal-delimiter
// boundaries. Every TD opens a fresh access unit and all OBUs until the next
// TD belong to it (including tile-only / show-existing frames). A
// frame-header OBU inside an AU supplies its KEY_FRAME random-access decision;
// sequence-header OBUs are stashed as keyframe configuration.
func assembleAV1(res *ScanResult, units []NALUnit) {
	var (
		cur *AccessUnit
		cfg []byte
	)
	flush := func() {
		// Unlike Annex B, every temporal-delimiter-opened AU is retained even
		// when it carries no frame-header OBU of its own.
		if cur == nil {
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
	for _, u := range units {
		switch u.Kind {
		case kindTemporalDelimiter:
			// A TD is an authoritative picture boundary; close any prior AU
			// and open a new one anchored at the delimiter.
			flush()
			cur = &AccessUnit{Offset: u.Offset}
		case kindSequenceHeader:
			cfg = append(cfg[:0], u.Data...)
			if cur != nil {
				cur.NALs = append(cur.NALs, u)
			}
		case kindFrameHeader:
			if cur == nil {
				cur = &AccessUnit{Offset: u.Offset}
			}
			cur.NALs = append(cur.NALs, u)
			if u.IsRAP {
				cur.IsRAP = true
			}
		default:
			if cur != nil {
				cur.NALs = append(cur.NALs, u)
			}
		}
	}
	flush()
}

// hasVCL reports whether an in-progress access unit contains a coded picture
// NAL/OBU (Annex B VCL or an AV1 frame header).
func hasVCL(au *AccessUnit) bool {
	for _, n := range au.NALs {
		if n.Kind == kindVCL || n.Kind == kindFrameHeader {
			return true
		}
	}
	return false
}
