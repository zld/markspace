package main

import (
	"encoding/binary"
	"math"
	"math/rand"
)

// This file generates the offline regression samples: a PCM WAV buffer,
// a length-framed RTP trace and MP4 metadata. Everything is derived
// from the seed, so repeated runs with identical flags produce
// identical bytes.

// corruptFlags selects which defects the generated samples carry.
type corruptFlags struct {
	tail      bool // physical file ends mid-block
	truncated bool // declared data length exceeds the physical file
}

func parseCorrupt(list []string) corruptFlags {
	var c corruptFlags
	for _, s := range list {
		switch s {
		case "tail":
			c.tail = true
		case "truncated":
			c.truncated = true
		}
	}
	return c
}

// generateWAV builds a 16-bit PCM sine buffer with the requested
// corruptions applied.
func generateWAV(seed int64, channels int, rate int, c corruptFlags) []byte {
	rng := rand.New(rand.NewSource(seed))
	frames := rate // one second of audio
	align := channels * 2
	declared := uint32(frames * align)
	if c.truncated {
		declared += 4096 // declared data runs past the physical end
	}
	buf := []byte("RIFF")
	buf = binary.LittleEndian.AppendUint32(buf, 36+declared)
	buf = append(buf, "WAVE"...)
	buf = append(buf, "fmt "...)
	buf = binary.LittleEndian.AppendUint32(buf, 16)
	buf = binary.LittleEndian.AppendUint16(buf, 1)
	buf = binary.LittleEndian.AppendUint16(buf, uint16(channels))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(rate))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(rate*align))
	buf = binary.LittleEndian.AppendUint16(buf, uint16(align))
	buf = binary.LittleEndian.AppendUint16(buf, 16)
	buf = append(buf, "data"...)
	buf = binary.LittleEndian.AppendUint32(buf, declared)
	for i := 0; i < frames; i++ {
		for ch := 0; ch < channels; ch++ {
			v := 0.5 * math.Sin(2*math.Pi*440*float64(i)/float64(rate)+float64(ch))
			v += 0.002 * (rng.Float64() - 0.5) // deterministic dither
			if v > 1 {
				v = 1
			}
			buf = binary.LittleEndian.AppendUint16(buf, uint16(int16(v*32767)))
		}
	}
	if c.tail {
		buf = buf[:len(buf)-3] // physical file ends inside the last block
	}
	return buf
}

// generateRTP builds a length-framed trace with normal packets,
// duplicates, in-window reordering, one late packet, a 32-bit timestamp
// wrap, a variable frame rate stretch and a drift-exceeding region.
func generateRTP(seed int64, rate int) []byte {
	rng := rand.New(rand.NewSource(seed + 1))
	const packets = 90
	normal := uint32(rate / 100) // 10 ms frames
	vfr := uint32(rate / 50)     // 20 ms frames in the VFR stretch
	startTS := uint32(0xFFFFFFFF - 5000)
	type ev struct {
		seq uint16
		ts  uint32
	}
	var events []ev
	var cum uint64
	var driftAcc float64
	for i := 0; i < packets; i++ {
		frame := normal
		if i >= 30 && i < 45 {
			frame = vfr // variable frame rate region
		}
		ts := startTS + uint32(cum)
		if i >= 60 && i < 80 {
			// Sender clock runs 600 ppm fast inside this region.
			driftAcc += float64(frame) * 600 / 1e6
			ts += uint32(driftAcc)
		}
		events = append(events, ev{seq: uint16(i + 1), ts: ts})
		cum += uint64(frame)
	}
	// Emission order: swap 5/6 (in-window reorder), hold 8 back until
	// after 20 (late), duplicate 10 and 25.
	var order []ev
	held := events[7]
	order = append(order, events[0:4]...)
	order = append(order, events[5], events[4], events[6])
	order = append(order, events[8:10]...)
	order = append(order, events[9]) // duplicate of seq 10
	order = append(order, events[10:20]...)
	order = append(order, held) // seq 8 arrives outside the window
	order = append(order, events[20:25]...)
	order = append(order, events[24]) // duplicate of seq 25
	order = append(order, events[25:]...)
	var out []byte
	for _, e := range order {
		payload := make([]byte, 4+rng.Intn(12))
		rng.Read(payload)
		pkt := make([]byte, 12+len(payload))
		pkt[0] = 0x80 // V=2
		pkt[1] = 96   // dynamic payload type
		binary.BigEndian.PutUint16(pkt[2:4], e.seq)
		binary.BigEndian.PutUint32(pkt[4:8], e.ts)
		binary.BigEndian.PutUint32(pkt[8:12], 0xBEEF)
		copy(pkt[12:], payload)
		out = binary.BigEndian.AppendUint16(out, uint16(len(pkt)))
		out = append(out, pkt...)
	}
	return out
}

// generateMP4 builds ftyp+moov(mvhd) metadata carrying the media
// timescale, mirroring a packaged stream's clock advertisement.
func generateMP4(timescale int) []byte {
	mvhd := []byte{0, 0, 0, 0} // version + flags
	mvhd = append(mvhd, make([]byte, 8)...)
	mvhd = binary.BigEndian.AppendUint32(mvhd, uint32(timescale))
	mvhd = binary.BigEndian.AppendUint32(mvhd, 0)
	mvhd = append(mvhd, make([]byte, 4+2+10+36+24+4)...)
	box := func(tag string, payload []byte) []byte {
		b := binary.BigEndian.AppendUint32(nil, uint32(8+len(payload)))
		return append(append(b, tag...), payload...)
	}
	moov := box("moov", box("mvhd", mvhd))
	return append(box("ftyp", []byte("isom\x00\x00\x00\x00")), moov...)
}
