package pipeline

import (
	"mediadist/internal/codec"
	"mediadist/internal/demux"
)

// configRecord builds the decoder configuration record from the parameter
// sets actually present in the first sample. The parameter-set bytes are
// copied verbatim; only the record framing is synthesized. Building it from
// the stream (rather than a hardcoded blob) keeps the record consistent with
// the media and avoids an unparsable hvcC.
func configRecord(c demux.Codec, samples []demux.Sample) []byte {
	if len(samples) == 0 {
		return nil
	}
	units := samples[0].Scan.Units
	data := samples[0].Data
	switch c {
	case demux.CodecH264:
		return buildAvcC(units, data)
	case demux.CodecH265:
		return buildHvcC(units, data)
	case demux.CodecAV1:
		// av1C: marker+version, profile 0, level 0x0C, no config OBUs. AV1
		// decoder configuration is carried in-band by the sequence header, so
		// the record stays minimal.
		return []byte{0x81, 0x00, 0x0C, 0x00}
	}
	return nil
}

// unitPayload returns the bytes of a unit excluding its start code.
func unitPayload(u codec.Unit, data []byte) []byte {
	start := u.Offset + u.HeaderLen
	end := start + u.Size
	if start < 0 || end > int64(len(data)) || start >= end {
		return nil
	}
	return data[start:end]
}

// buildAvcC assembles an AVCDecoderConfigurationRecord from the first SPS and
// PPS in the sample.
func buildAvcC(units []codec.Unit, data []byte) []byte {
	var sps, pps []byte
	for _, u := range units {
		switch u.Type {
		case 7:
			if sps == nil {
				sps = unitPayload(u, data)
			}
		case 8:
			if pps == nil {
				pps = unitPayload(u, data)
			}
		}
	}
	if len(sps) < 4 || len(pps) == 0 {
		return nil
	}
	out := []byte{0x01, sps[1], sps[2], sps[3], 0xFF, 0xE1}
	out = appendU16(out, len(sps))
	out = append(out, sps...)
	out = append(out, 0x01)
	out = appendU16(out, len(pps))
	out = append(out, pps...)
	return out
}

// buildHvcC assembles an HEVCDecoderConfigurationRecord. Field widths follow
// ISO/IEC 14496-15 section 8.3.3.1.1; the general_* fields are copied from
// the SPS so the record describes the actual media.
func buildHvcC(units []codec.Unit, data []byte) []byte {
	var vps, sps, pps []byte
	for _, u := range units {
		switch u.Type {
		case 32:
			if vps == nil {
				vps = unitPayload(u, data)
			}
		case 33:
			if sps == nil {
				sps = unitPayload(u, data)
			}
		case 34:
			if pps == nil {
				pps = unitPayload(u, data)
			}
		}
	}
	if len(vps) < 3 || len(sps) < 3 || len(pps) < 2 {
		return nil
	}
	out := []byte{0x01}
	// general_profile_space(2)=0, tier(1)=0, profile_idc(5) from SPS byte 1.
	out = append(out, sps[1]&0x1F)
	// general_profile_compatibility_flags: 32 bits copied from SPS bytes 2..5
	// when present, otherwise zero.
	compat := []byte{0, 0, 0, 0}
	if len(sps) >= 6 {
		copy(compat, sps[2:6])
	}
	out = append(out, compat...)
	// general_constraint_indicator_flags: 48 bits from SPS bytes 6..11.
	constr := make([]byte, 6)
	if len(sps) >= 12 {
		copy(constr, sps[6:12])
	}
	out = append(out, constr...)
	// general_level_idc from SPS byte 12 when present.
	if len(sps) >= 13 {
		out = append(out, sps[12])
	} else {
		out = append(out, 0)
	}
	// min_spatial_segmentation_idc with the reserved high nibble set.
	out = append(out, 0xF0, 0x00)
	// parallelismType with reserved high bits.
	out = append(out, 0xFC)
	// chromaFormat: use 4:2:0 (01) with reserved bits.
	out = append(out, 0xFD)
	// bitDepthLumaMinus8 and bitDepthChromaMinus8, both 0 for 8-bit.
	out = append(out, 0xF8, 0xF8)
	// avgFrameRate.
	out = append(out, 0x00, 0x00)
	// constantFrameRate(2)=0, numTemporalLayers(2)=0, temporalIdNested(1)=0,
	// lengthSizeMinusOne(2)=3.
	out = append(out, 0x0F)
	arrays := []struct {
		typ  byte
		nalu []byte
	}{
		{32, vps}, {33, sps}, {34, pps},
	}
	out = append(out, byte(len(arrays)))
	for _, a := range arrays {
		// completeness(1)=1, reserved(1)=0, NAL_unit_type(6).
		out = append(out, 0x80|(a.typ&0x3F))
		out = appendU16(out, 1)
		out = appendU16(out, len(a.nalu))
		out = append(out, a.nalu...)
	}
	return out
}

func appendU16(dst []byte, v int) []byte {
	return append(dst, byte(v>>8), byte(v))
}
