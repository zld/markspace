package testdata

import (
	"encoding/binary"
	"math"
)

func putU16(b []byte, v uint16) { binary.BigEndian.PutUint16(b, v) }
func putU32(b []byte, v uint32) { binary.BigEndian.PutUint32(b, v) }
func putI32(b []byte, v int32)  { binary.BigEndian.PutUint32(b, uint32(v)) }

// box wraps payload in a 32-bit size box.
func box(typ string, payload []byte) []byte {
	hdr := make([]byte, 8)
	putU32(hdr, uint32(8+len(payload)))
	copy(hdr[4:], typ)
	return append(hdr, payload...)
}

// fullbox prepends version+flags.
func fullbox(ver byte, flags uint32, payload []byte) []byte {
	hdr := make([]byte, 4)
	hdr[0] = ver
	hdr[1] = byte(flags >> 16)
	hdr[2] = byte(flags >> 8)
	hdr[3] = byte(flags)
	return append(hdr, payload...)
}

func fixed1616(v float64) uint32 { return uint32(math.Round(v * 65536.0)) }

// rotationMatrix returns the tkhd 9x int32 matrix for the given degrees.
// Layout is the standard a,b,u,c,d,v,x,y,w row-major 3x3 transform.
func rotationMatrix(deg int) [9]int32 {
	one := int32(0x00010000)
	switch deg {
	case 90:
		return [9]int32{0, one, 0, -one, 0, 0, 0, 0, 0x40000000}
	case 180:
		return [9]int32{-one, 0, 0, 0, -one, 0, 0, 0, 0x40000000}
	case 270:
		return [9]int32{0, -one, 0, one, 0, 0, 0, 0, 0x40000000}
	default:
		return [9]int32{one, 0, 0, 0, one, 0, 0, 0, 0x40000000}
	}
}
func tkhd(trackID uint32, timescale uint32, durationUnits uint64, w, h int, deg int) []byte {
	p := make([]byte, 0, 84)
	p = append(p, 0, 0, 0, 0) // version/flags
	tmp := make([]byte, 4)
	putU32(tmp, 0)
	p = append(p, tmp...) // creation
	p = append(p, tmp...) // modification
	putU32(tmp, trackID)
	p = append(p, tmp...)
	putU32(tmp, 0)
	p = append(p, tmp...) // reserved
	putU32(tmp, uint32(durationUnits))
	p = append(p, tmp...)
	p = append(p, make([]byte, 8)...)       // reserved
	p = append(p, make([]byte, 4)...)       // layer
	p = append(p, make([]byte, 4)...)       // alternate group
	p = append(p, make([]byte, 4)...)       // volume + reserved
	for _, v := range rotationMatrix(deg) { // 9 x int32
		putI32(tmp, v)
		p = append(p, tmp...)
	}
	putU32(tmp, fixed1616(float64(w)))
	p = append(p, tmp...)
	putU32(tmp, fixed1616(float64(h)))
	p = append(p, tmp...)
	return box("tkhd", p)
}

func mdhd(timescale uint32, durationUnits uint64) []byte {
	p := make([]byte, 0, 24)
	p = append(p, 0, 0, 0, 0) // version/flags
	tmp := make([]byte, 4)
	putU32(tmp, 0)
	p = append(p, tmp...) // creation
	p = append(p, tmp...) // modification
	putU32(tmp, timescale)
	p = append(p, tmp...)
	putU32(tmp, uint32(durationUnits))
	p = append(p, tmp...)
	p = append(p, []byte{0x55, 0xc4, 0, 0}...) // language 'und', pre_defined
	return box("mdhd", p)
}

func hdlr(handler string) []byte {
	p := make([]byte, 0, 25)
	p = append(p, 0, 0, 0, 0)
	p = append(p, 0, 0, 0, 0)
	p = append(p, handler...)
	p = append(p, make([]byte, 12)...)
	p = append(p, 0) // empty name
	return box("hdlr", p)
}

func colrBox(c ColorSpec) []byte {
	p := []byte{'n', 'c', 'l', 'x'}
	tmp := make([]byte, 2)
	putU16(tmp, c.Primaries)
	p = append(p, tmp...)
	putU16(tmp, c.Transfer)
	p = append(p, tmp...)
	putU16(tmp, c.Matrix)
	p = append(p, tmp...)
	flag := byte(0)
	if c.FullRange {
		flag = 0x80
	}
	p = append(p, flag, 0)
	return box("colr", p)
}

func clapBox(c [4]int64, w, h int) []byte {
	cw := w - int(c[0]+c[2])
	ch := h - int(c[1]+c[3])
	p := make([]byte, 0, 32)
	tmp := make([]byte, 4)
	for _, v := range []uint32{uint32(cw), 1, uint32(ch), 1,
		uint32(c[2] - c[0]), 1, uint32(c[3] - c[1]), 1} {
		putU32(tmp, v)
		p = append(p, tmp...)
	}
	return box("clap", p)
}

func videoEntry(spec MP4Spec) []byte {
	p := make([]byte, 0, 78)
	p = append(p, make([]byte, 6)...) // reserved
	p = append(p, 0, 0, 0, 1)         // data reference index
	p = append(p, make([]byte, 16)...)
	tmp := make([]byte, 2)
	putU16(tmp, uint16(spec.Width))
	p = append(p, tmp...)
	putU16(tmp, uint16(spec.Height))
	p = append(p, tmp...)
	tmp4 := make([]byte, 4)
	putU32(tmp4, 0x00480000) // 72 dpi
	p = append(p, tmp4...)
	p = append(p, tmp4...)
	p = append(p, 0, 0, 0, 0) // reserved
	p = append(p, 0, 1)       // frame count
	p = append(p, make([]byte, 32)...)
	p = append(p, 0, 24) // depth
	p = append(p, 0xff, 0xff)
	p = append(p, box("avcC", validAVCC(spec.Width, spec.Height))...)
	for _, c := range spec.Colors {
		p = append(p, colrBox(c)...)
	}
	p = append(p, clapBox(spec.Crop, spec.Width, spec.Height)...)
	return box("avc1", p)
}

// validAVCC builds a minimal decodable AVC decoder configuration record for
// the given frame size (baseline profile, level 3.0) so standard tooling can
// inspect the fixture even though sample payloads are synthetic luma bytes.
func validAVCC(width, height int) []byte {
	spsRBSP := buildSPS(width, height)
	spsNAL := append([]byte{0x67}, emulationEscape(spsRBSP)...)
	ppsNAL := []byte{0x68, 0xce, 0x38, 0x80}
	out := []byte{
		1,       // configuration version
		0x42,    // AVC baseline profile
		0,       // profile compatibility
		0x1e,    // level 3.0
		0xff,    // 6 bits reserved + NAL length size minus one (=4 bytes)
		0xe1,    // 3 bits reserved + number of SPS (=1)
	}
	tmp := make([]byte, 2)
	putU16(tmp, uint16(len(spsNAL)))
	out = append(out, tmp...)
	out = append(out, spsNAL...)
	out = append(out, 1) // number of PPS
	putU16(tmp, uint16(len(ppsNAL)))
	out = append(out, tmp...)
	out = append(out, ppsNAL...)
	return out
}

// buildSPS creates a baseline-profile SPS RBSP with the given dimensions.
func buildSPS(width, height int) []byte {
	w := writeBits{}
	w.u(8, 66)  // profile_idc baseline
	w.u(8, 0)   // constraint flags
	w.u(8, 30)  // level_idc
	w.ue(0)     // seq_parameter_set_id
	w.ue(0)     // log2_max_frame_num_minus4
	w.ue(0)     // pic_order_cnt_type
	w.ue(0)     // log2_max_pic_order_cnt_lsb_minus4
	w.ue(1) // max_num_ref_frames
	w.u(1, 0) // gaps_in_frame_num_allowed
	w.ue(uint64((width+15)/16 - 1))  // pic_width_in_mbs_minus1
	w.ue(uint64((height+15)/16 - 1)) // pic_height_in_map_units_minus1
	w.u(1, 1) // frame_mbs_only_flag
	w.u(1, 0)   // direct_8x8_inference_flag
	w.u(1, 0)   // frame_cropping_flag
	w.u(1, 0)   // vui_parameters_present_flag
	w.rbspStop()
	return w.bytes()
}

type writeBits struct {
	buf []byte
	cur byte
	n   uint
}

func (w *writeBits) u(bits uint, v uint64) {
	for i := int(bits) - 1; i >= 0; i-- {
		w.cur = (w.cur << 1) | byte((v>>uint(i))&1)
		w.n++
		if w.n == 8 {
			w.buf = append(w.buf, w.cur)
			w.cur, w.n = 0, 0
		}
	}
}

// ue writes an unsigned Exp-Golomb value.
func (w *writeBits) ue(v uint64) {
	code := v + 1
	zeros := uint(0)
	for x := code; x > 1; x >>= 1 {
		zeros++
	}
	w.u(zeros, 0)
	w.u(zeros+1, code)
}

func (w *writeBits) rbspStop() {
	w.u(1, 1)
	if w.n > 0 {
		w.cur <<= 8 - w.n
		w.buf = append(w.buf, w.cur)
		w.cur, w.n = 0, 0
	}
}

func (w *writeBits) bytes() []byte { return w.buf }

// emulationEscape inserts 0x03 after forbidden 00 00 / 00 00 01 patterns.
func emulationEscape(b []byte) []byte {
	var out []byte
	zeros := 0
	for _, x := range b {
		if zeros >= 2 && x <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		out = append(out, x)
		if x == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

func audioEntry(rate uint32) []byte {
	p := make([]byte, 0, 28)
	p = append(p, make([]byte, 6)...)
	p = append(p, 0, 0, 0, 1)
	p = append(p, make([]byte, 8)...)
	p = append(p, 0, 1)  // channels
	p = append(p, 0, 16) // sample size
	p = append(p, 0, 0, 0, 0)
	tmp := make([]byte, 4)
	putU32(tmp, rate<<16)
	p = append(p, tmp...)
	return box("mp4a", p)
}

func concat(parts [][]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func pcmBytes(pcm []int16) []byte {
	out := make([]byte, len(pcm)*2)
	for i, v := range pcm {
		binary.LittleEndian.PutUint16(out[i*2:], uint16(v))
	}
	return out
}
