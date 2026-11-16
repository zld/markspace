// Package demo generates deterministic synthetic multi-bitrate MPEG-TS
// inputs and injects faults, so the pipeline can be demonstrated and
// verified without external media files.
package demo

import (
	"fmt"
	"io"
	"math/rand"

	"hlscheck/internal/ts"
)

// Well-known PIDs of the synthetic multiplex (variables, not
// constants, so byte truncation in section building is allowed).
var (
	pidPAT   = uint16(0x0000)
	pidPMT   = uint16(0x1000)
	pidVideo = uint16(0x0101)
)

const fps = 25

// GenOptions describes one synthetic input profile.
type GenOptions struct {
	BitrateKbps int
	DurationSec float64
	GOPSec      float64 // keyframe interval; <= 0 means no keyframes
	Seed        int64
	StartPTS    int64 // 90 kHz; defaults to 1 h
}

// Generate writes a deterministic MPEG-TS stream: PAT/PMT before every
// keyframe, one PES per frame, an IDR NAL every GOPSec seconds. All
// profiles share the same PTS timeline so outputs stay aligned.
func Generate(w io.Writer, opt GenOptions) error {
	rnd := rand.New(rand.NewSource(opt.Seed))
	start := opt.StartPTS
	if start == 0 {
		start = ts.ClockRate * 3600
	}
	nFrames := int(opt.DurationSec * fps)
	if nFrames < 1 {
		return fmt.Errorf("duration %.2fs too short", opt.DurationSec)
	}
	frameBytes := opt.BitrateKbps * 1000 / 8 / fps
	if frameBytes < 16 {
		frameBytes = 16
	}
	gop := int(opt.GOPSec * fps)
	cc := map[uint16]int{}
	for f := 0; f < nFrames; f++ {
		key := gop > 0 && f%gop == 0
		if key {
			if err := writePacket(w, cc, pidPAT, true, patSection()); err != nil {
				return err
			}
			if err := writePacket(w, cc, pidPMT, true, pmtSection()); err != nil {
				return err
			}
		}
		pts := start + int64(f)*ts.ClockRate/fps
		es := make([]byte, frameBytes)
		if _, err := rnd.Read(es); err != nil {
			return err
		}
		nal := byte(0x41) // non-IDR slice
		if key {
			nal = 0x65 // IDR
		}
		copy(es, []byte{0, 0, 0, 1, nal})
		if err := writePES(w, cc, pts, es); err != nil {
			return err
		}
	}
	return nil
}

// writePES packetizes one PES (header + elementary stream) into TS
// packets on the video PID.
func writePES(w io.Writer, cc map[uint16]int, pts int64, es []byte) error {
	hdr := []byte{0, 0, 1, 0xE0, 0, 0, 0x80, 0x80, 0x05}
	hdr = ts.EncodePTS(hdr, pts)
	pes := append(hdr, es...)
	for first := true; len(pes) > 0; first = false {
		n := min(len(pes), 184)
		if err := writePacket(w, cc, pidVideo, first, pes[:n]); err != nil {
			return err
		}
		pes = pes[n:]
	}
	return nil
}

// writePacket emits exactly one 188-byte packet, padding short payloads
// with an adaptation field.
func writePacket(w io.Writer, cc map[uint16]int, pid uint16, pusi bool, payload []byte) error {
	if len(payload) > 184 {
		return fmt.Errorf("payload too large: %d", len(payload))
	}
	var pkt [ts.PacketSize]byte
	pkt[0] = ts.SyncByte
	pkt[1] = byte(pid >> 8)
	if pusi {
		pkt[1] |= 0x40
	}
	pkt[2] = byte(pid)
	c := cc[pid]
	cc[pid] = (c + 1) & 0x0F
	stuff := 184 - len(payload)
	switch {
	case stuff == 0:
		pkt[3] = 0x10 | byte(c) // payload only
		copy(pkt[4:], payload)
	case stuff == 1:
		pkt[3] = 0x30 | byte(c) // adaptation (length 0) + payload
		pkt[4] = 0
		copy(pkt[5:], payload)
	default:
		pkt[3] = 0x30 | byte(c)
		pkt[4] = byte(stuff - 1)
		for i := 5; i < 4+stuff; i++ {
			pkt[i] = 0xFF
		}
		copy(pkt[4+stuff:], payload)
	}
	_, err := w.Write(pkt[:])
	return err
}

func patSection() []byte {
	return []byte{
		0x00,             // pointer field
		0x00,             // table id: PAT
		0xB0, 0x0D,       // section length 13
		0x00, 0x01,       // transport stream id
		0xC1, 0x00, 0x00, // version, section numbers
		0x00, 0x01, // program 1
		0xE0 | byte(pidPMT>>8), byte(pidPMT),
		0, 0, 0, 0, // CRC (not validated)
	}
}

func pmtSection() []byte {
	return []byte{
		0x00,             // pointer field
		0x02,             // table id: PMT
		0xB0, 0x12,       // section length 18
		0x00, 0x01,       // program number
		0xC1, 0x00, 0x00, // version, section numbers
		0xE0 | byte(pidVideo>>8), byte(pidVideo), // PCR PID
		0xF0, 0x00, // program info length 0
		0x1B, // stream type: H.264
		0xE0 | byte(pidVideo>>8), byte(pidVideo),
		0xF0, 0x00, // ES info length 0
		0, 0, 0, 0, // CRC (not validated)
	}
}
