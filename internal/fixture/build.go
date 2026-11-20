package fixture

import (
	"fmt"
	"io"
)

// Profiles selects the sample file layout to generate.
const (
	ProfileEditWrap       = "edit-wrap"       // classic, edit list + B frames
	ProfileFragmentedWrap = "fragmented-wrap" // moof/mdat, tfdt v0 wrap
	ProfileTruncated      = "truncated"       // cut mid-box
	ProfileOOB            = "oob"             // sample offset out of bounds
)

// Build writes the requested profile to w. Output is deterministic.
func Build(profile string, w io.Writer) error {
	var data []byte
	var err error
	switch profile {
	case ProfileEditWrap:
		data = buildEditWrap()
	case ProfileFragmentedWrap:
		data = buildFragmentedWrap()
	case ProfileTruncated:
		full := buildEditWrap()
		data = full[:len(full)-137] // cut inside moov
	case ProfileOOB:
		data = corruptStco(buildEditWrap())
	default:
		return fmt.Errorf("fixture: unknown profile %q", profile)
	}
	if err == nil {
		_, err = w.Write(data)
	}
	return err
}

// corruptStco rewrites the first stco entry to point past EOF.
func corruptStco(data []byte) []byte {
	out := append([]byte(nil), data...)
	needle := []byte("stco")
	for i := 0; i+4+12 <= len(out); i++ {
		if string(out[i:i+4]) == string(needle) {
			entry := i + 12 // type + version/flags + count
			out[entry] = 0x7F
			out[entry+1] = 0xFF
			out[entry+2] = 0x00
			out[entry+3] = 0x00
			return out
		}
	}
	return out
}

// --- shared box builders ---

func mvhd(timescale, duration, nextTrackID uint32) []byte {
	var p bw
	p.u32(0) // creation
	p.u32(0) // modification
	p.u32(timescale)
	p.u32(duration)
	p.u32(0x00010000) // rate
	p.u16(0x0100)     // volume
	p.u16(0)
	p.zeros(8)
	matrix(&p)
	p.zeros(24) // pre_defined
	p.u32(nextTrackID)
	return fullBoxOf("mvhd", 0, 0, p.buf)
}

func matrix(p *bw) {
	for _, v := range []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000} {
		p.u32(v)
	}
}

func tkhd(trackID, duration, width, height uint32, audio bool) []byte {
	var p bw
	p.u32(0)
	p.u32(0)
	p.u32(trackID)
	p.u32(0)
	p.u32(duration)
	p.zeros(8)
	p.u16(0) // layer
	p.u16(0) // alternate group
	if audio {
		p.u16(0x0100)
	} else {
		p.u16(0)
	}
	p.u16(0)
	matrix(&p)
	p.u32(width << 16)
	p.u32(height << 16)
	return fullBoxOf("tkhd", 0, 0x000003, p.buf)
}

func mdhd(timescale, duration uint32) []byte {
	var p bw
	p.u32(0)
	p.u32(0)
	p.u32(timescale)
	p.u32(duration)
	p.u16(0x55C4) // language "und"
	p.u16(0)
	return fullBoxOf("mdhd", 0, 0, p.buf)
}

func hdlr(kind string) []byte {
	var p bw
	p.u32(0) // pre_defined
	p.str(kind)
	p.zeros(12)
	if kind == "vide" {
		p.str("VideoHandler")
	} else {
		p.str("SoundHandler")
	}
	p.u8(0)
	return fullBoxOf("hdlr", 0, 0, p.buf)
}

func dinf() []byte {
	var p bw
	p.u32(1) // entry count
	url := fullBoxOf("url ", 0, 1, nil)
	p.bytes(url)
	return boxOf("dinf", fullBoxOf("dref", 0, 0, p.buf))
}

func vmhd() []byte {
	var p bw
	p.u16(0) // graphicsmode
	p.zeros(6)
	return fullBoxOf("vmhd", 0, 1, p.buf)
}

func smhd() []byte {
	var p bw
	p.u16(0)
	p.u16(0)
	return fullBoxOf("smhd", 0, 0, p.buf)
}

var fakeAvcC = []byte{
	1, 100, 0, 40, 0xFF, 0xE1,
	0x00, 0x0C, // SPS length 12
	0x67, 0x64, 0x00, 0x28, 0xAC, 0xD9, 0x40, 0x78, 0x02, 0x27, 0xE5, 0xC0,
	0x01,       // one PPS
	0x00, 0x04, // PPS length 4
	0x68, 0xEE, 0x3C, 0x80,
}

func stsdAvc1(width, height uint16) []byte {
	var e bw
	e.zeros(6)
	e.u16(1) // data reference index
	e.zeros(16)
	e.u16(width)
	e.u16(height)
	e.u32(0x00480000) // horizresolution
	e.u32(0x00480000) // vertresolution
	e.u32(0)
	e.u16(1) // frame count
	e.zeros(32)
	e.u16(0x0018) // depth
	e.u16(0xFFFF)
	e.bytes(boxOf("avcC", fakeAvcC))
	entry := boxOf("avc1", e.buf)
	var p bw
	p.u32(1)
	p.bytes(entry)
	return fullBoxOf("stsd", 0, 0, p.buf)
}

func stsdMp4a(sampleRate uint32) []byte {
	var e bw
	e.zeros(6)
	e.u16(1)
	e.zeros(8)
	e.u16(2)  // channels
	e.u16(16) // sample size
	e.u16(0)
	e.u16(0)
	e.u32(sampleRate << 16)
	entry := boxOf("mp4a", e.buf)
	var p bw
	p.u32(1)
	p.bytes(entry)
	return fullBoxOf("stsd", 0, 0, p.buf)
}

// sttsFrom builds an stts from (count, delta) pairs.
func sttsFrom(pairs [][2]uint32) []byte {
	var p bw
	p.u32(uint32(len(pairs)))
	for _, pr := range pairs {
		p.u32(pr[0])
		p.u32(pr[1])
	}
	return fullBoxOf("stts", 0, 0, p.buf)
}

func stscFrom(firstChunk, perChunk uint32) []byte {
	var p bw
	p.u32(1)
	p.u32(firstChunk)
	p.u32(perChunk)
	p.u32(1)
	return fullBoxOf("stsc", 0, 0, p.buf)
}

func stszFrom(sizes []uint32) []byte {
	var p bw
	p.u32(0) // not uniform
	p.u32(uint32(len(sizes)))
	for _, s := range sizes {
		p.u32(s)
	}
	return fullBoxOf("stsz", 0, 0, p.buf)
}

func stcoFrom(offsets []uint32) []byte {
	var p bw
	p.u32(uint32(len(offsets)))
	for _, o := range offsets {
		p.u32(o)
	}
	return fullBoxOf("stco", 0, 0, p.buf)
}

func emptyStblEntry() []byte {
	var p bw
	p.u32(0)
	return p.buf
}
