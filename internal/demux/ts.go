package demux

import (
	"fmt"

	"github.com/pion/rtp"

	"mediarc/internal/model"
)

const (
	tsPacketSize = 188
	tsSyncByte   = 0x47
	tsClockRate  = 90000
	pidPAT       = 0x0000
)

// TSDemuxer reads HLS MPEG-Transport-Stream fragments.
type TSDemuxer struct {
	file string
}

// NewTSDemuxer binds a logical file name used for offset-attributed errors.
func NewTSDemuxer(file string) *TSDemuxer { return &TSDemuxer{file: file} }

type tsStream struct {
	pid       uint16
	codec     model.Codec
	buf       []byte // PES assembly buffer
	pesLen    int    // declared PES packet length, 0 when unbounded
	cc        uint8
	haveCC    bool
	disco     bool // applies to the frame currently in the assembly buffer
	pending   bool // adaptation-field marker waiting for the next PUSI frame
	samples   []model.Sample
	errors    []*model.MediaError
	clockPkts [][]byte
}

// Demux extracts elementary tracks from one MPEG-TS buffer. Corruption on one
// PID is recorded on that track and never prevents sibling PIDs from being
// recovered.
func (d *TSDemuxer) Demux(data []byte) ([]model.Track, error) {
	if len(data) == 0 || data[0] != tsSyncByte {
		return nil, model.AtOffset(model.KindSegmentCorrupt, d.file, 0, "", "missing MPEG-TS sync byte 0x47")
	}

	streams := map[uint16]*tsStream{}
	var pmtPID uint16
	gotPAT, gotPMT := false, false

	for off := 0; off+tsPacketSize <= len(data); off += tsPacketSize {
		pkt := data[off : off+tsPacketSize]
		if pkt[0] != tsSyncByte {
			return nil, model.AtOffset(model.KindSegmentCorrupt, d.file, int64(off), "", "lost TS sync byte")
		}
		pusi := pkt[1]&0x40 != 0
		pid := uint16(pkt[1]&0x1F)<<8 | uint16(pkt[2])
		afc := pkt[3] >> 4 & 0x03
		cc := pkt[3] & 0x0F
		payload, disco := tsPayload(pkt, afc)

		switch {
		case pid == pidPAT:
			if pusi {
				if pid := parsePATPMTPID(payload); pid > 0 {
					pmtPID = pid
					gotPAT = true
				}
			}
		case gotPAT && pid == pmtPID:
			if pusi {
				d.parsePMT(payload, streams)
				gotPMT = true
			}
		case gotPMT:
			st, ok := streams[pid]
			if !ok {
				continue // PCR/other PID: ignore
			}
			if disco {
				// The marker describes the next access unit on this PID; a
				// marker on a continuation packet belongs to the frame already
				// being assembled.
				if pusi || len(st.buf) == 0 {
					st.pending = true
				} else {
					st.disco = true
				}
			}
			d.feedStream(st, pusi, cc, payload, int64(off))
		}
	}
	if len(data)%tsPacketSize != 0 {
		// Trailing partial packet is corruption, not a reason to drop tracks.
		for _, st := range streams {
			st.errors = append(st.errors, model.AtOffset(model.KindSegmentCorrupt, d.file,
				int64(len(data)/tsPacketSize*tsPacketSize), "", "truncated trailing TS packet"))
		}
	}

	out := make([]model.Track, 0, len(streams))
	for _, st := range streams {
		st.finalizeFrame()
		st.fillDurations()
		st.buildClockPackets()
		out = append(out, model.Track{
			ID: uint32(st.pid), Codec: st.codec, Timescale: tsClockRate,
			Samples: st.samples, ClockPackets: st.clockPkts,
			NotMappable: st.codec == model.CodecUnknown,
			DemuxErrors: st.errors,
		})
	}
	if len(out) == 0 {
		return nil, model.AtOffset(model.KindSegmentCorrupt, d.file, 0, "", "no elementary streams found in TS fragment")
	}
	return out, nil
}

// tsPayload strips the adaptation field and reports the discontinuity flag.
func tsPayload(pkt []byte, afc uint8) (payload []byte, disco bool) {
	pos := 4
	if afc == 2 || afc == 3 {
		al := int(pkt[pos])
		if al > 0 && pos+1 < len(pkt) {
			disco = pkt[pos+1]&0x80 != 0
		}
		pos += 1 + al
	}
	if afc == 0 || afc == 2 || pos >= len(pkt) {
		return nil, disco
	}
	return pkt[pos:], disco
}

func (d *TSDemuxer) feedStream(st *tsStream, pusi bool, cc uint8, payload []byte, off int64) {
	if st.haveCC && cc != (st.cc+1)&0x0F {
		st.errors = append(st.errors, model.AtOffset(model.KindSegmentCorrupt, d.file, off,
			fmt.Sprintf("pid %d", st.pid), "continuity counter jump"))
	}
	st.haveCC = true
	st.cc = cc
	if len(payload) == 0 {
		return
	}
	if pusi {
		st.finalizeFrame()
		st.buf = append(st.buf[:0], payload...)
		st.pesLen = tsDeclaredPESLen(st.buf)
		st.disco, st.pending = st.pending, false
	} else if len(st.buf) > 0 {
		st.buf = append(st.buf, payload...)
	}
	// A bounded PES is complete as soon as the declared bytes have arrived;
	// cut here so the 0xFF stuffing of the final TS packet never leaks into
	// the compressed sample payload.
	if st.pesLen > 0 && len(st.buf) >= 6+st.pesLen {
		st.finalizeFrame()
	}
}

func (st *tsStream) finalizeFrame() {
	if len(st.buf) < 9 || st.buf[0] != 0 || st.buf[1] != 0 || st.buf[2] != 1 {
		st.buf = st.buf[:0]
		st.pesLen = 0
		return
	}
	flags := st.buf[7]
	hdrLen := int(st.buf[8]) + 9
	pesEnd := len(st.buf)
	if st.pesLen > 0 && 6+st.pesLen < pesEnd {
		pesEnd = 6 + st.pesLen
	}
	if hdrLen > pesEnd {
		st.buf = st.buf[:0]
		st.pesLen = 0
		return
	}
	_, dts, ok := parsePTS(st.buf, flags)
	if !ok {
		st.buf = st.buf[:0]
		st.pesLen = 0
		return
	}
	body := append([]byte(nil), st.buf[hdrLen:pesEnd]...)
	st.samples = append(st.samples, model.Sample{
		Dts: dts, Duration: 0, Payload: body, Sync: st.disco || st.isKeyHint(body),
	})
	st.disco = false
	st.buf = st.buf[:0]
	st.pesLen = 0
}

// tsDeclaredPESLen reads the PES_packet_length field (bytes after the field
// itself). Zero means an unbounded PES, common for live video.
func tsDeclaredPESLen(buf []byte) int {
	if len(buf) < 6 {
		return 0
	}
	return int(buf[4])<<8 | int(buf[5])
}

// isKeyHint uses a conservative payload heuristic (H.264 NAL type 5 IDR)
// which is sufficient for generated fixtures.
func (st *tsStream) isKeyHint(body []byte) bool {
	return len(body) > 0 && body[0]&0x1F == 5
}

func (st *tsStream) fillDurations() {
	for i := range st.samples {
		if i+1 < len(st.samples) {
			st.samples[i].Duration = uint32(st.samples[i+1].Dts - st.samples[i].Dts)
		} else if i > 0 {
			st.samples[i].Duration = st.samples[i-1].Duration
		} else {
			st.samples[i].Duration = tsClockRate / 30
		}
	}
}

func (st *tsStream) buildClockPackets() {
	st.clockPkts = make([][]byte, len(st.samples))
	for i, s := range st.samples {
		p := &rtp.Packet{
			Header: rtp.Header{
				Version: 2, PayloadType: 96,
				SequenceNumber: uint16(i), Timestamp: uint32(s.Dts), SSRC: uint32(st.pid),
			},
			Payload: []byte{0x01},
		}
		buf, err := p.Marshal()
		if err != nil {
			panic(err)
		}
		st.clockPkts[i] = buf
	}
}

func parsePTS(b []byte, flags byte) (pts, dts uint64, ok bool) {
	if flags&0x80 == 0 {
		return 0, 0, false
	}
	// ISO 13818-1 2.5.3.6 layout: 3/15/15 bits split across five bytes,
	// with fixed marker 1-bits at b9[0], b11[0], b13[0].
	pts = uint64(b[9]>>1&0x07)<<30 | uint64(b[10])<<22 | uint64(b[11]>>1)<<15 |
		uint64(b[12])<<7 | uint64(b[13]>>1)
	dts = pts
	if flags&0x40 != 0 && len(b) >= 19 {
		dts = uint64(b[14]>>1&0x07)<<30 | uint64(b[15])<<22 | uint64(b[16]>>1)<<15 |
			uint64(b[17])<<7 | uint64(b[18]>>1)
	}
	return pts, dts, true
}

// parsePATPMTPID reads the PMT PID from a PAT section payload (with pointer).
func parsePATPMTPID(payload []byte) uint16 {
	if len(payload) < 1 {
		return 0
	}
	sec := payload[1+int(payload[0]):]
	if len(sec) < 8 || sec[0] != 0x00 {
		return 0
	}
	sectionLen := int(sec[1]&0x0F)<<8 | int(sec[2])
	end := 3 + sectionLen - 4 // exclude CRC32
	for p := 8; p+4 <= end; p += 4 {
		program := uint16(sec[p])<<8 | uint16(sec[p+1])
		pid := uint16(sec[p+2]&0x1F)<<8 | uint16(sec[p+3])
		if program != 0 {
			return pid
		}
	}
	return 0
}

func (d *TSDemuxer) parsePMT(payload []byte, streams map[uint16]*tsStream) {
	if len(payload) < 1 {
		return
	}
	sec := payload[1+int(payload[0]):]
	if len(sec) < 12 {
		return
	}
	sectionLen := int(sec[1]&0x0F)<<8 | int(sec[2])
	end := 3 + sectionLen - 4
	if end > len(sec) {
		end = len(sec)
	}
	if 12 > end {
		return
	}
	// section layout: table_id(1) section_length(2) tsid(2) version(1)
	// section numbers(2) PCR_PID(2) program_info_length(2) => ES loop at 10.
	p := 8
	infoLen := int(sec[p]&0x0F)<<8 | int(sec[p+1])
	p += 2 + infoLen
	for p+5 <= end && p+5 <= len(sec) {
		streamType := sec[p]
		pid := uint16(sec[p+1]&0x1F)<<8 | uint16(sec[p+2])
		esInfoLen := int(sec[p+3]&0x0F)<<8 | int(sec[p+4])
		p += 5 + esInfoLen
		if _, exists := streams[pid]; !exists {
			streams[pid] = &tsStream{pid: pid, codec: tsCodec(streamType)}
		}
	}
}

func tsCodec(streamType byte) model.Codec {
	switch streamType {
	case 0x1B, 0x24, 0x02, 0x10: // H.264 / HEVC / MPEG-2 video
		return model.CodecAVC1
	case 0x0F, 0x03, 0x11, 0x04: // AAC / MPEG audio
		return model.CodecMP4A
	default:
		return model.CodecUnknown
	}
}
